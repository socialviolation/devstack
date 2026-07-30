package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hooks"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerHooksTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("hooks",
		mcp.WithDescription("Inspect and test lifecycle hooks: shell commands devstack runs automatically when a stack or service changes state. Use action=\"list\" to see what is wired up before you create or destroy a stack, so you know what will run and what external state it touches. Use action=\"run\" to fire an event's hooks WITHOUT performing the lifecycle action — the way to test a hook, or to retry provisioning after a setup hook failed and left a stack created-but-unprovisioned.\n\n"+
			"You do not call this to make hooks happen during normal work: stack_create, stack_up, stack_down, stack_rm, start and stop fire their own events and report the result. This tool is for seeing what exists and re-running it.\n\n"+
			"Hooks are declared in devstack.workspace.yaml (shared with the team; a 'services:' list scopes one to named services) and in a service's devstack.service.yaml (that service only). A feature stack inherits the workspace's hooks.\n\n"+
			"Events, each with a fixed firing point: "+strings.Join(config.HookEvents(), ", ")+". "+
			"stack.create fires once ports are allocated and the record written; stack.destroy fires before any of it is removed, so a teardown hook can still read what the stack was allocated.\n\n"+
			"A hook that fails on a SETUP event (stack.create, stack.up, service.start, workspace.up) aborts the action's remaining hooks and is reported as an error — the resource exists but is not fully provisioned. A hook that fails on a TEARDOWN event (stack.destroy, stack.down, service.stop, workspace.down) is reported and skipped, so a broken hook can never leave a stack that cannot be removed."),
		mcp.WithString("action",
			mcp.Description("\"list\" (default) shows declared hooks and the events that fire them. \"run\" fires an event's hooks now, without performing the lifecycle action itself.")),
		mcp.WithString("event",
			mcp.Description("Event name. Required for action=\"run\". Optional filter for action=\"list\". One of: "+strings.Join(config.HookEvents(), ", "))),
		mcp.WithString("stack",
			mcp.Description("Feature stack SHORT name to resolve against, so ${self.url} and ${self.port.<key>} resolve to THAT stack's allocated ports rather than base's pinned ones. Omit (or pass \"base\") for the base workspace.")),
		mcp.WithString("services",
			mcp.Description("Comma-separated service names forming the event's service set, for action=\"run\". Defaults to the stack's overlay, or every service in the workspace.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved for hooks"), nil
		}
		action := strings.TrimSpace(request.GetString("action", "list"))
		if action == "" {
			action = "list"
		}
		stackName := strings.TrimSpace(request.GetString("stack", ""))
		event := strings.TrimSpace(request.GetString("event", ""))
		if event != "" && !isHookEvent(event) {
			return mcp.NewToolResultError(fmt.Sprintf("unknown event %q; known events: %s", event, strings.Join(config.HookEvents(), ", "))), nil
		}

		switch action {
		case "list":
			return hooksList(ws, stackName, event)
		case "run":
			if event == "" {
				return mcp.NewToolResultError(fmt.Sprintf("action=\"run\" needs an event; one of: %s", strings.Join(config.HookEvents(), ", "))), nil
			}
			return hooksRun(ws, stackName, event, splitServices(request.GetString("services", "")))
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q; use \"list\" or \"run\"", action)), nil
		}
	})
}

func hooksList(ws *workspace.Workspace, stackName, eventFilter string) (*mcp.CallToolResult, error) {
	events := config.HookEvents()
	if eventFilter != "" {
		events = []string{eventFilter}
	}

	target := "base"
	if stackName != "" && stackName != "base" {
		target = "stack " + stackName
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Hooks for workspace %s (%s)\n", ws.Name, target)

	total := 0
	for _, event := range events {
		ev, src, err := hooks.Context(ws, stackName, event, nil)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		invocations := hooks.Resolve(ev, src)
		if len(invocations) == 0 {
			continue
		}
		total += len(invocations)
		fmt.Fprintf(&sb, "\n%s\n", event)
		for _, inv := range invocations {
			fmt.Fprintf(&sb, "  %-28s declared in %s (onError=%s, timeout=%s)\n",
				inv.Label(), inv.Origin, inv.Hook.ResolvedOnError(event), inv.Hook.ResolvedTimeout())
			fmt.Fprintf(&sb, "      run: %s\n", inv.Hook.Run)
		}
	}

	if total == 0 {
		fmt.Fprintf(&sb, "\nNo hooks are declared for this workspace, so no lifecycle automation runs.\n")
		fmt.Fprintf(&sb, "Declare them in %s (optionally scoped with 'services:'), or in a service's %s.\n",
			config.WorkspaceManifestFileName, config.ServiceManifestFileName)
		fmt.Fprintf(&sb, "Events: %s\n", strings.Join(config.HookEvents(), ", "))
		return mcp.NewToolResultText(sb.String()), nil
	}

	sb.WriteString("\nA hook labelled name/service runs once for that service, with it as ${self}.\n")
	sb.WriteString("These fire on their own when you call stack_create, stack_up, stack_down, stack_rm, start or stop. Use action=\"run\" only to test one or retry a failure.\n")
	return mcp.NewToolResultText(sb.String()), nil
}

func hooksRun(ws *workspace.Workspace, stackName, event string, services []string) (*mcp.CallToolResult, error) {
	ev, src, err := hooks.Context(ws, stackName, event, services)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	invocations := hooks.Resolve(ev, src)
	if len(invocations) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf(
			"No hooks fire on %s for %s, so nothing ran. This is not an error — the workspace simply declares none for that event. Check with action=\"list\".",
			event, ev.StackLabel())), nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Fired %s for %s (services: %s)\n", event, ev.StackLabel(), strings.Join(ev.Services, ", "))
	results, runErr := hooks.Run(ev, src, &out)

	failed := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}
	fmt.Fprintf(&out, "\n%d hook(s) ran, %d failed.\n", len(results), failed)
	if runErr != nil {
		fmt.Fprintf(&out, "FAILED: %v\n", runErr)
		return mcp.NewToolResultError(out.String()), nil
	}
	return mcp.NewToolResultText(out.String()), nil
}

func isHookEvent(name string) bool {
	for _, e := range config.HookEvents() {
		if e == name {
			return true
		}
	}
	return false
}

func splitServices(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
