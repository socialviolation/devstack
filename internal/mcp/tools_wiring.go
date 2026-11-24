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
			"For each key it shows the value that base's copy of A resolves to, and the copy of B that value reaches. Pass stack to add that stack's copy of A beside base.\n\n"+
			"\"stack X A → base B\" means the stack's A still reaches base's B. That is normal when the stack does not change B. \"base A → stack X B\" means base reaches into a stack. "+
			"Read both before you change config: they show what a change moves. An env key that names a service which does not exist is an error. "+
			"A note says an edge the workspace manifest declares has no key that carries it, or a key that reaches a service with no declared edge. "+
			"This reads the env ladder of each checkout. Use topology for the declared graph alone, and service_env to see or set a value. Values that hold a credential are masked."),
		mcp.WithString("service",
			mcp.Description("Exact service name. Shows only the edges into and out of it. Omit it for every edge.")),
		mcp.WithString("stack",
			mcp.Description(optionalStackNameDesc+" Adds that stack's copy of each service beside base.")),
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
	base := wiring.Copy{Label: "base", Env: baseEnv, Book: config.BuildPortBook(baseRW)}

	services := make([]string, 0, len(baseRW.Services))
	deps := map[string][]string{}
	for name := range baseRW.Services {
		services = append(services, name)
		deps[name] = baseRW.Manifest.ResourceDeps(name)
	}
	sort.Strings(services)
	if service != "" && baseRW.Services[service].Name == "" {
		return "", fmt.Errorf("unknown service %q. Services: %s", service, strings.Join(services, ", "))
	}

	var stackCopy *wiring.Copy
	if stackName != "" && !strings.EqualFold(stackName, "base") {
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
	return renderWiring(m, service, stackCopy, showBase), nil
}

func renderWiring(m wiring.Map, service string, stackCopy *wiring.Copy, showBase bool) string {
	var sb strings.Builder
	label := ""
	if stackCopy != nil {
		label = stackCopy.Label
		fmt.Fprintf(&sb, "Wiring of base and stack %s\n", label)
	} else {
		sb.WriteString("Wiring of base\n")
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
			fmt.Fprintf(&sb, "\n%s\n", edge)
		}
		declared := ""
		if !l.Declared {
			declared = "  (undeclared)"
		}
		fmt.Fprintf(&sb, "  %s  via %s%s\n", l.Key, l.Via, declared)
		writeHop(&sb, "base", l.Key, l.To, l.Base, showBase)
		if stackCopy != nil {
			if _, runs := stackCopy.Env[l.From]; !runs {
				fmt.Fprintf(&sb, "    %s: runs base's copy of %s\n", label, l.From)
			} else {
				writeHop(&sb, label, l.Key, l.To, l.Stack, true)
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
		sb.WriteString("\nBase values are hidden. Only the owner sees them.\n")
	}
	return sb.String()
}

func writeHop(sb *strings.Builder, copyLabel, key, to string, hop *wiring.Hop, showValue bool) {
	if hop == nil {
		fmt.Fprintf(sb, "    %s: does not set %s\n", copyLabel, key)
		return
	}
	target := "base " + to
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
