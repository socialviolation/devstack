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
		mcp.WithDescription("Inspect and test lifecycle hooks. A hook is a shell command that devstack runs on its own when a stack or a service changes state.\n"+
			"Use action=\"list\" to see what is wired up before you create or destroy a stack. It tells you what will run, and what external state it touches. "+
			"Use action=\"run\" to fire an event's hooks WITHOUT the lifecycle action. That is how you test a hook. It is also how you retry provisioning after a setup hook failed and left a stack created and unprovisioned.\n\n"+
			"You do not call this tool to make hooks happen during normal work. stack_create, stack_add, stack_up, stack_down, stack_rm, start and stop fire their own events and report the result. This tool is for seeing what exists, and for running it again. "+
			"stack_add is the one worth knowing: it fires stack.create for the services it added, and it fires stack.up as well when the stack is already up. It fires neither for the services already in the stack.\n\n"+
			"You declare hooks in devstack.workspace.yaml, which the team shares. A 'services:' list there scopes a hook to named services. You also declare hooks in a service manifest, and those cover that service only. A service manifest is devstack.service.yaml, or devstack.<name>.yaml for each service after the first one in that directory. A feature stack inherits the workspace's hooks.\n\n"+
			"A hook's command can name values that it can not know in advance, because devstack allocates a stack's ports when it creates the stack. "+
			"${self.url} is the http URL of the service the hook runs for. ${self.port.<key>} is one of that service's ports, by manifest key. ${<service>.url} and ${<service>.port.<key>} are the same for another service in the event. "+
			"'self' is the service, and not the stack. A hook also receives DEVSTACK_* environment variables, and the whole event as JSON on stdin.\n\n"+
			"Events, each with a fixed firing point: "+strings.Join(config.HookEvents(), ", ")+". "+
			"stack.create fires once the worktrees exist, the ports are allocated and the record is written. stack.destroy fires before devstack removes any of that, so a teardown hook can still read what the stack was allocated.\n\n"+
			"A hook that fails on a SETUP event (stack.create, stack.up, service.start, workspace.up) skips the event's remaining hooks, and the tool returns it as an error. devstack does NOT roll the lifecycle action back: the stack exists, and it is not fully provisioned. "+
			"Fix the hook and run the event again here with action=\"run\", rather than create the stack again. The hooks themselves decide what happens to a hook that already succeeded, because devstack fires all of them again. So write a hook that provisions external state to tolerate a second run.\n\n"+
			"A hook that fails on a TEARDOWN event (stack.destroy, stack.down, service.stop, workspace.down) is reported and skipped, and the teardown still completes. So a broken hook can never leave a stack that nobody can remove. The external state that the hook was to clean up is probably still there. "+
			"stack.destroy is the one failure with NO retry: the removal deletes the record that ${self...} resolves against. devstack prints the resolved URLs at the point of failure, and somebody has to do the cleanup by hand. Pass that on, rather than report a clean teardown."),
		mcp.WithString("action",
			mcp.Enum("list", "run"),
			mcp.Description("\"list\" is the default. It prints every declared hook, with the event that fires it, the file that declares it, its error policy, its timeout, and the shell command it runs word for word. Read that before you trigger it, to see what external state it touches. \"run\" fires an event's hooks now, and it does not perform the lifecycle action itself.")),
		mcp.WithString("event",
			mcp.Enum(config.HookEvents()...),
			mcp.Description("Event name. Required for action=\"run\". Optional filter for action=\"list\".")),
		mcp.WithString("stack",
			mcp.Description("Feature stack SHORT name to resolve against, so that a hook's ${self...} references resolve to THAT stack's allocated ports, and not to base's pinned ones. For the base workspace, omit this parameter or pass \"base\".")),
		mcp.WithString("services",
			mcp.Description("Comma-separated service names that form the event's service set, for action=\"run\". The default is the stack's overlay, or every service in the workspace.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace for hooks"), nil
		}
		action := strings.TrimSpace(request.GetString("action", "list"))
		if action == "" {
			action = "list"
		}
		stackName := strings.TrimSpace(request.GetString("stack", ""))
		event := strings.TrimSpace(request.GetString("event", ""))
		if event != "" && !isHookEvent(event) {
			return mcp.NewToolResultError(fmt.Sprintf("unknown event %q. Known events: %s", event, strings.Join(config.HookEvents(), ", "))), nil
		}

		switch action {
		case "list":
			return hooksList(ws, stackName, event)
		case "run":
			if event == "" {
				return mcp.NewToolResultError(fmt.Sprintf("action=\"run\" needs an event. Pass one of: %s", strings.Join(config.HookEvents(), ", "))), nil
			}
			return hooksRun(ws, stackName, event, splitServices(request.GetString("services", "")))
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q. Use \"list\" or \"run\"", action)), nil
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
		fmt.Fprintf(&sb, "Declare them in %s, where a 'services:' list can scope them, or in a service's %s.\n",
			config.WorkspaceManifestFileName, config.ServiceManifestFileName)
		fmt.Fprintf(&sb, "Events: %s\n", strings.Join(config.HookEvents(), ", "))
		return mcp.NewToolResultText(sb.String()), nil
	}

	sb.WriteString("\nA hook labelled name/service runs once for that service, with it as ${self}.\n")
	sb.WriteString("These fire on their own when you call stack_create, stack_up, stack_down, stack_rm, start or stop. Use action=\"run\" only to test one, or to retry a failure.\n")
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
			"No hooks fire on %s for %s, so nothing ran. This is not an error. The workspace declares none for that event. To see the declared hooks, use action=\"list\".",
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
