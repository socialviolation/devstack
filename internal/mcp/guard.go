package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

type rule int

const (
	deny rule = iota
	allow
	ownStack
)

const agentStackPrefix = "agent-"

type toolPolicy struct {
	StackArg string
	Actions  map[string]rule
	Default  rule
}

func (p toolPolicy) ruleFor(action string) rule {
	if r, ok := p.Actions[action]; ok {
		return r
	}
	return p.Default
}

func (p toolPolicy) visible() bool {
	if p.Default != deny {
		return true
	}
	for _, r := range p.Actions {
		if r != deny {
			return true
		}
	}
	return false
}

var agentPolicies = map[string]toolPolicy{
	"environment":   {Default: allow},
	"status":        {Default: allow},
	"urls":          {Default: allow},
	"topology":      {Default: allow},
	"process_logs":  {Default: allow},
	"investigate":   {Default: allow},
	"stack_list":    {Default: allow},
	"start":         {StackArg: "stack", Default: ownStack},
	"restart":       {StackArg: "stack", Default: ownStack},
	"stop":          {StackArg: "stack", Default: ownStack},
	"stack_config":  {StackArg: "stack", Default: ownStack},
	"env_which":     {StackArg: "stack", Default: ownStack},
	"stack_create":  {StackArg: "name", Default: ownStack},
	"stack_add":     {StackArg: "name", Default: ownStack},
	"stack_remove":  {StackArg: "name", Default: ownStack},
	"stack_note":    {StackArg: "name", Default: ownStack},
	"stack_up":      {StackArg: "name", Default: ownStack},
	"stack_down":    {StackArg: "name", Default: ownStack},
	"stack_rm":      {StackArg: "name", Default: ownStack},
	"stack_extend":  {StackArg: "name", Default: ownStack},
	"configure":     {Default: deny},
	"tunnel":        {Default: deny},
	"env_set":       {Default: deny},
	"env_use":       {Default: deny},
	"migrate":       {Default: deny},
	"base":          {Actions: map[string]rule{"path": allow}, Default: deny},
	"observability": {Actions: map[string]rule{"status": allow, "variants": allow}, Default: deny},
	"hooks":         {StackArg: "stack", Actions: map[string]rule{"": allow, "list": allow, "run": ownStack}, Default: deny},
	"service_env": {StackArg: "stack", Actions: map[string]rule{
		"get": ownStack, "diff": ownStack, "check": ownStack, "drift": ownStack,
	}, Default: deny},
}

var mutating sync.Mutex

type AgentLimits struct {
	MaxStacks int
	TTL       time.Duration
}

var DefaultAgentLimits = AgentLimits{MaxStacks: 2, TTL: 24 * time.Hour}

type limitsKey struct{}

func agentLimitsFrom(ctx context.Context) AgentLimits {
	if l, ok := ctx.Value(limitsKey{}).(AgentLimits); ok {
		return l
	}
	return DefaultAgentLimits
}

type guard struct {
	ws     *workspace.Workspace
	limits AgentLimits
	server *server.MCPServer
}

func GuardOptions(ws *workspace.Workspace, limits AgentLimits) []server.ServerOption {
	g := &guard{ws: ws, limits: limits}
	return []server.ServerOption{
		func(s *server.MCPServer) { g.server = s },
		server.WithToolHandlerMiddleware(g.middleware),
		server.WithToolFilter(g.filter),
	}
}

func (g *guard) middleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !g.readOnly(req.Params.Name) {
			mutating.Lock()
			defer mutating.Unlock()
		}
		if caller := auth.CallerFrom(ctx); caller.Scope != auth.ScopeOwner {
			if reason := g.check(caller, req); reason != "" {
				return mcp.NewToolResultError(reason), nil
			}
		}
		return next(context.WithValue(ctx, limitsKey{}, g.limits), req)
	}
}

func (g *guard) filter(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	if auth.CallerFrom(ctx).Scope == auth.ScopeOwner {
		return tools
	}
	out := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if p, ok := agentPolicies[t.Name]; ok && p.visible() {
			out = append(out, t)
		}
	}
	return out
}

func (g *guard) readOnly(name string) bool {
	if g.server == nil {
		return false
	}
	t := g.server.GetTool(name)
	if t == nil || t.Tool.Annotations.ReadOnlyHint == nil {
		return false
	}
	return *t.Tool.Annotations.ReadOnlyHint
}

func (g *guard) check(caller auth.Caller, req mcp.CallToolRequest) string {
	name := req.Params.Name
	p, ok := agentPolicies[name]
	if !ok {
		return fmt.Sprintf("agents may not call %s", name)
	}
	action := strings.ToLower(strings.TrimSpace(req.GetString("action", "")))
	switch p.ruleFor(action) {
	case allow:
		return ""
	case ownStack:
		return g.checkOwnStack(caller, p.StackArg, strings.TrimSpace(req.GetString(p.StackArg, "")))
	}
	if action != "" {
		return fmt.Sprintf("agents may not call %s with action=%q", name, action)
	}
	return fmt.Sprintf("agents may not call %s", name)
}

func (g *guard) checkOwnStack(caller auth.Caller, arg, name string) string {
	hint := fmt.Sprintf("agents may only act on their own agent-* stacks; pass %s=agent-<name>", arg)
	if name == "" || strings.EqualFold(name, "base") {
		return hint
	}
	if !strings.HasPrefix(strings.ToLower(name), agentStackPrefix) {
		return fmt.Sprintf("%q is not an agent-* stack. %s", name, hint)
	}
	if g.ws == nil {
		return "devstack resolved no workspace, so it can not check who owns the stack"
	}
	recs, err := stack.LoadStore(g.ws.Name)
	if err != nil {
		return fmt.Sprintf("devstack can not read the stacks store, so it can not check who owns the stack: %v", err)
	}
	for _, r := range recs {
		if strings.EqualFold(r.Name, name) && r.Owner != "" && r.Owner != caller.Name {
			return fmt.Sprintf("stack %q belongs to %s. %s", r.Name, r.Owner, hint)
		}
	}
	return ""
}
