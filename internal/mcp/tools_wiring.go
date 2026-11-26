package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/wiring"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerWiringTool(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("wiring",
		mcp.WithDescription("Show how each service reaches the services it talks to. For every edge A → B it names the env key of A that carries the address of B, and how devstack knows: a ${B.url}, ${B.host} or ${B.port.<key>} reference, a ${stack.suffix:B} messaging name, or a literal port that B listens on. "+
			"For each key it shows the value that A resolves to with no stack, and the service that value reaches. Pass stack to add that stack's value of each key.\n\n"+
			"\"shared B\" means the shared service B. \"B at its declared port\" means a port where no stack serves B. If a stack's A reaches B at its declared port, nothing answers there: add B to the stack with stack_add. "+
			"\"A with no stack → stack X B\" means a value outside the stack points into stack X. "+
			"Read both before you change config: they show what a change moves. An env key that names a service which does not exist is an error. "+
			"Each edge shows its declared kind: needs or calls, and the via value if the edge has one. "+
			"A note says a declared edge has no key that carries it, or a key that reaches a service with no declared edge. "+
			"This reads the env ladder of each checkout and each stack worktree. Use topology for the declared graph alone, and service_env to see or set a value. Values that hold a credential are masked."),
		mcp.WithString("service",
			mcp.Description("Exact service name. Shows only the edges into and out of it. Omit it for every edge.")),
		mcp.WithString("stack",
			mcp.Description(optionalStackNameDesc+" Adds that stack's value of each key.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		showBase := auth.CallerFrom(ctx).Scope == auth.ScopeOwner
		out, err := wiringReport(ws, workspacePath,
			strings.TrimSpace(request.GetString("stack", "")),
			strings.TrimSpace(request.GetString("service", "")),
			showBase)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	})
}

func wiringReport(ws *workspace.Workspace, workspacePath, stackName, service string, showBase bool) (string, error) {
	baseRW, err := config.ResolveWorkspace(workspacePath)
	if err != nil {
		return "", err
	}
	baseEnv, err := wiring.RawEnv(baseRW, "")
	if err != nil {
		return "", err
	}
	base := wiring.Copy{Label: "no stack", Env: baseEnv, Book: config.BuildPortBook(baseRW)}

	services := make([]string, 0, len(baseRW.Services))
	deps := map[string][]string{}
	edges := map[[2]string]string{}
	for name := range baseRW.Services {
		services = append(services, name)
		for _, kind := range []struct {
			verb  string
			edges []config.ServiceEdge
		}{{"needs", baseRW.Needs(name)}, {"calls", baseRW.CallEdges(name)}} {
			for _, e := range kind.edges {
				deps[name] = append(deps[name], e.Service)
				if _, ok := edges[[2]string{name, e.Service}]; ok {
					continue
				}
				label := kind.verb
				if e.Via != "" {
					label += ", via " + e.Via
				}
				edges[[2]string{name, e.Service}] = label
			}
		}
	}
	sort.Strings(services)
	if service != "" && baseRW.Services[service].Name == "" {
		return "", fmt.Errorf("unknown service %q. Services: %s", service, strings.Join(services, ", "))
	}

	var stackCopy *wiring.Copy
	if stackName != "" {
		rec, err := resolveStackRecord(ws, stackName)
		if err != nil {
			return "", err
		}
		rw, err := stack.ResolveWorktree(rec)
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(rw.Services))
		for n := range rw.Services {
			names = append(names, n)
		}
		opts, err := stack.GenerateOptions(rec, names)
		if err != nil {
			return "", err
		}
		env, err := wiring.RawEnv(rw, rec.Env)
		if err != nil {
			return "", err
		}
		stackCopy = &wiring.Copy{Label: rec.Name, Env: env, Book: opts.Book, Scope: opts.Stack}
	}

	m := wiring.Derive(services, deps, base, stackCopy)
	return renderWiring(m, edges, service, stackCopy, showBase, baseRW.IsShared), nil
}

func renderWiring(m wiring.Map, edges map[[2]string]string, service string, stackCopy *wiring.Copy, showBase bool, isShared func(string) bool) string {
	var sb strings.Builder
	label := ""
	if stackCopy != nil {
		label = stackCopy.Label
		fmt.Fprintf(&sb, "Wiring with no stack and in stack %s\n", label)
	} else {
		sb.WriteString("Wiring with no stack\n")
	}

	shown := 0
	prev := ""
	for _, l := range m.Links {
		if service != "" && l.From != service && l.To != service {
			continue
		}
		shown++
		if edge := l.From + " → " + l.To; edge != prev {
			prev = edge
			if kind := edges[[2]string{l.From, l.To}]; kind != "" {
				edge += "  (" + kind + ")"
			}
			fmt.Fprintf(&sb, "\n%s\n", edge)
		}
		declared := ""
		if !l.Declared {
			declared = "  (undeclared)"
		}
		fmt.Fprintf(&sb, "  %s  via %s%s\n", l.Key, l.Via, declared)
		writeHop(&sb, "no stack", l.Key, l.To, l.Base, showBase, isShared)
		if stackCopy != nil {
			if _, runs := stackCopy.Env[l.From]; !runs {
				fmt.Fprintf(&sb, "    %s: does not run %s\n", label, l.From)
			} else {
				writeHop(&sb, label, l.Key, l.To, l.Stack, true, isShared)
			}
		}
		if flag := l.Flag(label); flag != "" {
			fmt.Fprintf(&sb, "    ! %s\n", flag)
		}
	}
	if shown == 0 {
		sb.WriteString("\nNo env key carries the address of another service.\n")
	}

	var issues []wiring.Issue
	for _, is := range m.Issues {
		if service == "" || is.From == service || is.To == service {
			issues = append(issues, is)
		}
	}
	if len(issues) > 0 {
		sb.WriteString("\nIssues:\n")
		for _, is := range issues {
			fmt.Fprintf(&sb, "  - [%s] %s\n", is.Severity, is.Message)
		}
	}
	if !showBase {
		sb.WriteString("\nValues with no stack are hidden. Only the owner sees them.\n")
	}
	return sb.String()
}

func writeHop(sb *strings.Builder, copyLabel, key, to string, hop *wiring.Hop, showValue bool, isShared func(string) bool) {
	if hop == nil {
		fmt.Fprintf(sb, "    %s: does not set %s\n", copyLabel, key)
		return
	}
	target := to + " at its declared port"
	if isShared(to) {
		target = "shared " + to
	}
	if hop.Target != "base" {
		target = "stack " + hop.Target + " " + to
	}
	switch {
	case hop.Err != "":
		fmt.Fprintf(sb, "    %s: can not resolve: %s\n", copyLabel, hop.Err)
	case !showValue:
		fmt.Fprintf(sb, "    %s: → %s\n", copyLabel, target)
	default:
		fmt.Fprintf(sb, "    %s: %s → %s\n", copyLabel, svcconfig.RedactValue(key, hop.Value), target)
	}
}
