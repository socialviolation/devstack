package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

type rule int

const (
	deny rule = iota
	allow
	ownStack
	ownStackIfGiven
	ownStackOrSharedRead
	ownStackNotShared
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
	"wiring":        {StackArg: "stack", Default: ownStack},
	"process_logs":  {StackArg: "stack", Default: ownStackOrSharedRead},
	"investigate":   {Default: deny},
	"stack_list":    {Default: allow},
	"start":         {StackArg: "stack", Default: ownStackNotShared},
	"restart":       {StackArg: "stack", Default: ownStackNotShared},
	"stop":          {StackArg: "stack", Default: ownStackNotShared},
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
	"stack_resync":  {StackArg: "name", Default: ownStack},
	"stack_adopt":   {Default: deny},
	"task_run":      {StackArg: "stack", Default: ownStack},
	"doctor":        {StackArg: "stack", Default: ownStackIfGiven},
	"cleanup":       {Default: deny},
	"configure":     {Default: deny},
	"tunnel":        {Default: deny},
	"env_set":       {Default: deny},
	"env_use":       {Default: deny},
	"migrate":       {Default: deny},
	"shared":        {Actions: map[string]rule{"path": allow}, Default: deny},
	"observability": {Actions: map[string]rule{"status": allow, "variants": allow}, Default: deny},
	"hooks":         {StackArg: "stack", Actions: map[string]rule{"": allow, "list": allow, "run": ownStack}, Default: deny},
	"service_env": {StackArg: "stack", Actions: map[string]rule{
		"get": ownStack, "diff": ownStack, "check": ownStack, "drift": ownStack,
	}, Default: deny},
}

var mutating sync.Mutex

type guard struct {
	ws     *workspace.Workspace
	server *server.MCPServer
}

func GuardOptions(ws *workspace.Workspace) []server.ServerOption {
	g := &guard{ws: ws}
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
		return next(ctx, req)
	}
}

func (g *guard) filter(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	if auth.CallerFrom(ctx).Scope == auth.ScopeOwner {
		return tools
	}
	out := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if agentMaySee(t.Name) {
			out = append(out, t)
		}
	}
	return out
}

func agentMaySee(tool string) bool {
	p, ok := agentPolicies[tool]
	return ok && p.visible()
}

func visibleTools(ctx context.Context, tools []string) []string {
	if auth.CallerFrom(ctx).Scope == auth.ScopeOwner {
		return tools
	}
	var out []string
	for _, t := range tools {
		if agentMaySee(t) {
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
		return fmt.Sprintf("agents can not call %s", name)
	}
	action := strings.ToLower(strings.TrimSpace(req.GetString("action", "")))
	switch p.ruleFor(action) {
	case allow:
		return ""
	case ownStack:
		return g.checkOwnStack(caller, name, p.StackArg, strings.TrimSpace(req.GetString(p.StackArg, "")))
	case ownStackIfGiven:
		if given := strings.TrimSpace(req.GetString(p.StackArg, "")); given != "" {
			return g.checkOwnStack(caller, name, p.StackArg, given)
		}
		return ""
	case ownStackOrSharedRead:
		given := strings.TrimSpace(req.GetString(p.StackArg, ""))
		if given == "" && g.isShared(strings.TrimSpace(req.GetString("service", ""))) {
			return ""
		}
		return g.checkOwnStack(caller, name, p.StackArg, given)
	case ownStackNotShared:
		given := strings.TrimSpace(req.GetString(p.StackArg, ""))
		if svc := strings.TrimSpace(req.GetString("service", "")); given == "" && g.isShared(svc) {
			return fmt.Sprintf("agents can not call %s on shared service %q, because every stack uses it. Ask the owner", name, svc)
		}
		return g.checkOwnStack(caller, name, p.StackArg, given)
	}
	if action != "" {
		return fmt.Sprintf("agents can not call %s with action=%q", name, action)
	}
	return fmt.Sprintf("agents can not call %s", name)
}

func (g *guard) isShared(svc string) bool {
	if svc == "" || g.ws == nil {
		return false
	}
	m, err := config.LoadWorkspaceManifest(g.ws.Path)
	return err == nil && m.IsShared(svc)
}

func (g *guard) checkOwnStack(caller auth.Caller, tool, arg, name string) string {
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
		if !strings.EqualFold(r.Name, name) {
			continue
		}
		switch r.Owner {
		case caller.Name:
			return ""
		case "":
			return fmt.Sprintf("stack %q belongs to the owner, not to an agent. %s", r.Name, hint)
		}
		return fmt.Sprintf("stack %q belongs to %s. %s", r.Name, r.Owner, hint)
	}
	if tool == "stack_create" {
		return ""
	}
	return fmt.Sprintf("stack %q does not exist. Create it with stack_create first", name)
}
