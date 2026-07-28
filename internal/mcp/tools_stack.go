package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// stackShortNameDesc states the short-name-vs-identity rule wherever a stack is
// named: every parameter takes the short name, while output prints the identity.
const stackShortNameDesc = "Feature stack SHORT name (e.g. 'import-review') — not the '<base>--<name>' full identity that stack_list and telemetry print. There is no stack called \"base\": base is the absence of a stack, so omit this rather than passing \"base\" (the service-control and telemetry tools accept \"base\" as a synonym for omitting it; the stack tools do not, and stack_rm would reject it)."

// serviceLinksDesc defines the "service links" these tools return: one
// http://localhost:<port> URL per port the stack allocated.
const serviceLinksDesc = "A service link is 'service/portKey=http://localhost:<port>': the localhost URL of one port that this stack allocated for one of its overlay services, portKey being a key from that service's manifest ports (e.g. 'http'). These are the stack's own ports — reach its instances here, not on base's."

func registerStackTools(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	registerStackCreateTool(mcpServer, ws)
	registerStackListTool(mcpServer, ws)
	registerStackRemoveTool(mcpServer, ws)
	registerStackUpTool(mcpServer, ws)
	registerStackDownTool(mcpServer, ws)
}

func registerStackCreateTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_create",
		mcp.WithDescription("Create a feature stack overlaying THIS workspace (the base). A stack instantiates only the services it changes plus the services that call them, each in its own git worktree on a dynamically allocated port; every other service resolves to the base stack. Use this for the request 'I need a stack to work on X in services A and B'. The base workspace must be running — a stack reuses its services. Returns the overlay set with reasons, worktree paths, service links, and any warnings (dirty base checkout, base daemon not running). "+serviceLinksDesc),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Short stack name (e.g. 'import-review'). The full stack identity becomes '<base>--<name>'; every stack parameter across these tools takes the short name.")),
		mcp.WithString("repos", mcp.Required(),
			mcp.Description("Comma-separated exact service names this stack changes (e.g. 'frontend,backend'). Services that call these are pulled into the overlay automatically.")),
		mcp.WithString("note",
			mcp.Description("What this stack is for, in the author's words — a ticket URL, an issue key, a sentence. devstack never derives this: the branch says what changed, the note says why. Shown by stack_list. Optional, and editable later with the CLI: devstack stack note <name> \"...\"")),
		mcp.WithString("branch",
			mcp.Description("Git branch for the changed repos' worktrees. Created if absent, attached to if it already exists. Defaults to the stack name.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no base workspace resolved for stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		var repos []string
		for _, r := range strings.Split(request.GetString("repos", ""), ",") {
			if r = strings.TrimSpace(r); r != "" {
				repos = append(repos, r)
			}
		}
		if len(repos) == 0 {
			return mcp.NewToolResultError("repos must name at least one service this stack changes"), nil
		}

		res, err := stack.Create(stack.CreateInput{
			Note:   request.GetString("note", ""),
			Base:   ws,
			Name:   name,
			Repos:  repos,
			Branch: strings.TrimSpace(request.GetString("branch", "")),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Stack %q created (base %s).\n", res.StackName, res.BaseName)
		fmt.Fprintf(&sb, "Stack root: %s\n\n", res.StackRoot)

		sb.WriteString("Overlay set (changed ∪ transitive callers):\n")
		for _, m := range res.Overlay {
			reason := "calls a changed service"
			if m.Reason == "changed" {
				reason = "changed"
			}
			fmt.Fprintf(&sb, "  %-16s %s\n", m.Service, reason)
		}

		sb.WriteString("\nWorktrees:\n")
		for _, wt := range res.Worktrees {
			branchNote := "detached at HEAD"
			if wt.Branch != "" {
				branchNote = "branch " + wt.Branch
			}
			fmt.Fprintf(&sb, "  %-16s %s (%s)\n", wt.Service, wt.Path, branchNote)
		}

		if len(res.Ports) > 0 {
			sb.WriteString("\nAllocated ports (service/portKey):\n")
			for _, k := range sortedPortKeys(res.Ports) {
				fmt.Fprintf(&sb, "  %-24s http://localhost:%d\n", k, res.Ports[k])
			}
		}

		if len(res.Warnings) > 0 {
			sb.WriteString("\nWarnings:\n")
			for _, w := range res.Warnings {
				fmt.Fprintf(&sb, "  - %s\n", w)
			}
		}

		fmt.Fprintf(&sb, "\nStart it: (cd %s && devstack workspace up)\n", res.StackRoot)
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackListTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_list",
		mcp.WithDescription("List the feature stacks of THIS workspace: which services each one overlays (runs its own copy of), its branch, its env, the note saying what it is for, its allocated links, and its base, status (active = its services run in the host daemon; inactive = worktrees and record exist but nothing of it runs, so tools that act on running services error \"not up\" for it), the base daemon port their services run in when active, and allocated service links. The STACK column prints each stack's full identity '<base>--<name>' (the form telemetry and daemon resources use); every stack parameter across these tools takes the short '<name>' half. "+serviceLinksDesc),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved for stacks"), nil
		}
		stacks, err := stack.List(ws.Name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(stacks) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("No stacks in workspace %q.", ws.Name)), nil
		}
		var sb strings.Builder
		for _, s := range stacks {
			short := strings.TrimPrefix(s.Name, s.BaseName+"--")
			fmt.Fprintf(&sb, "%s (%s, base :%d)\n", short, s.Status, s.BasePort)
			services := "-"
			if len(s.Services) > 0 {
				services = strings.Join(s.Services, ", ")
			}
			fmt.Fprintf(&sb, "  overlays: %s\n", services)
			fmt.Fprintf(&sb, "  branch:   %s\n", s.Branch)
			if s.Env != "" {
				fmt.Fprintf(&sb, "  env:      %s\n", s.Env)
			}
			if s.Note != "" {
				fmt.Fprintf(&sb, "  note:     %s\n", s.Note)
			}
			links := make([]string, 0, len(s.Ports))
			for _, k := range sortedPortKeys(s.Ports) {
				links = append(links, fmt.Sprintf("%s=http://localhost:%d", k, s.Ports[k]))
			}
			if len(links) > 0 {
				fmt.Fprintf(&sb, "  links:    %s\n", strings.Join(links, " "))
			}
		}
		sb.WriteString("\noverlays are the services this stack runs its own copy of; every other service it borrows from base.\n")
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackRemoveTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_rm",
		mcp.WithDescription("Tear down a feature stack of THIS workspace: stop its daemon, remove its worktrees, release its ports, delete its record, and delete its stack root. Refuses a worktree with uncommitted changes unless force is set."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(stackShortNameDesc)),
		mcp.WithBoolean("force",
			mcp.Description("Remove worktrees even if they have uncommitted changes. Destroys uncommitted work. Defaults to false.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved for stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		force := request.GetBool("force", false)

		res, err := stack.Remove(ws, name, force)

		var sb strings.Builder
		if res != nil {
			fmt.Fprintf(&sb, "Removing stack %q (base %s)\n", res.Name, res.BaseName)
			for _, p := range res.RemovedWorktrees {
				fmt.Fprintf(&sb, "  removed worktree %s\n", p)
			}
			if res.PortsReleased {
				sb.WriteString("  released allocated ports\n")
			}
			if res.Deregistered {
				fmt.Fprintf(&sb, "  removed record for %q\n", res.Name)
			}
			if res.RootRemoved {
				fmt.Fprintf(&sb, "  removed stack root %s\n", res.StackRoot)
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(&sb, "  warning: %s\n", w)
			}
		}
		if err != nil {
			if sb.Len() > 0 {
				return mcp.NewToolResultError(sb.String() + "\n" + err.Error()), nil
			}
			return mcp.NewToolResultError(err.Error()), nil
		}
		fmt.Fprintf(&sb, "Stack %q removed.", res.Name)
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackUpTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_up",
		mcp.WithDescription("Bring a feature stack up: mark it (and its base workspace) active, fold its <base>:<service>:<stack> resources into the one host Tilt daemon, and ensure that daemon is running so it hot-reloads them. Mirrors 'devstack stack up'. This is the remedy when another tool reports the stack is not up: status, process_logs, restart, start, stop and configure all refuse an inactive stack rather than falling through to base. There is no per-stack daemon — its services run on their own ports inside the host daemon. Returns the stack's allocated service links and the daemon status. "+serviceLinksDesc),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(stackShortNameDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no base workspace resolved for stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		rec, err := stack.Resolve(ws.Name, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if err := workspace.SetWorkspaceActive(ws.Name, true); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to mark base workspace active: %v", err)), nil
		}
		if err := stack.SetActive(ws.Name, rec.Name, true); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := hostdaemon.Regenerate(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to regenerate host Tiltfile: %v", err)), nil
		}
		daemonMsg, err := hostdaemon.EnsureDaemon()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Stack %q is active in base %q; its services run in the host daemon on :%d as %s:<service>:%s.\n",
			rec.Name, ws.Name, workspace.HostTiltPort, ws.Name, rec.Name)
		if daemonMsg != "" {
			fmt.Fprintf(&sb, "%s\n", daemonMsg)
		}
		for _, k := range sortedPortKeys(rec.Ports) {
			fmt.Fprintf(&sb, "  %-24s http://localhost:%d\n", k, rec.Ports[k])
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackDownTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_down",
		mcp.WithDescription("Stop a feature stack's services in the host daemon: mark it inactive (nothing of it runs any more) and regenerate the host Tiltfile so the running daemon drops its <base>:<service>:<stack> resources. Mirrors 'devstack stack down'. Leaves the stack's worktrees and record intact (remove them with stack_rm)."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(stackShortNameDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no base workspace resolved for stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		rec, err := stack.Resolve(ws.Name, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if err := stack.SetActive(ws.Name, rec.Name, false); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := hostdaemon.Regenerate(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to regenerate host Tiltfile: %v", err)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf(
			"Stack %q is now inactive; the host daemon will drop its resources. Worktrees and record kept (remove with stack_rm %s).\n"+
				"While inactive, status/process_logs/restart/stop/configure targeting it error \"not up\" instead of falling through to base; "+
				"service_env still reads and writes its worktree config, and investigate returns only what it emitted while it was up.",
			rec.Name, rec.Name)), nil
	})
}

func sortedPortKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
