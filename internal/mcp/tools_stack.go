package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hooks"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
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
	registerStackNoteTool(mcpServer, ws)
	registerStackRemoveTool(mcpServer, ws)
	registerStackUpTool(mcpServer, ws)
	registerStackDownTool(mcpServer, ws)
}

func registerStackCreateTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_create",
		mcp.WithDescription("Create a feature stack overlaying THIS workspace (the base). A stack instantiates only the services it changes plus the services that call them, each in its own git worktree on a dynamically allocated port; every other service resolves to the base stack. Use this for the request 'I need a stack to work on X in services A and B'. The base workspace must be running — a stack reuses its services. Returns the overlay set with reasons, worktree paths, service links, and any warnings (dirty base checkout, base daemon not running). This workspace may declare lifecycle HOOKS: shell commands devstack runs automatically on this action, which can change state outside this machine (registering callback URLs, provisioning resources). They fire on their own and their output is included below. A hook failure is returned as an error and means the stack exists but is NOT fully provisioned — do not report success; see the hooks tool. Run the hooks tool with action=\"list\" first if you do not know what this workspace fires. "+serviceLinksDesc),
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

		overlay := make([]string, 0, len(res.Overlay))
		for _, m := range res.Overlay {
			overlay = append(overlay, m.Service)
		}
		var hookOut strings.Builder
		hookErr := hooks.Fire(ws, name, config.EventStackCreate, overlay, &hookOut)

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

		appendHookOutput(&sb, config.EventStackCreate, hookOut.String(), hookErr)
		if hookErr != nil {
			return mcp.NewToolResultError(sb.String()), nil
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

func registerStackNoteTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_note",
		mcp.WithDescription("Read or set what a feature stack is for. devstack derives everything else about a stack — which services it overlays, its branch, its age — but not why it exists, and a week later that is the part nobody can reconstruct. Free text: a ticket URL, an issue key, a sentence. Omit note to read the current one; pass an empty string to clear it. Shown by stack_list. Mirrors 'devstack stack note'."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(stackShortNameDesc)),
		mcp.WithString("note",
			mcp.Description("What the stack is for. Omit to read the current note instead of writing one; pass \"\" to clear it.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		rec, err := stack.FindStack(ws.Name, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		note, given := request.GetArguments()["note"]
		if !given {
			if rec.Note == "" {
				return mcp.NewToolResultText(fmt.Sprintf("Stack %q has no note. Set one so the next reader knows what it is for.", rec.Name)), nil
			}
			return mcp.NewToolResultText(rec.Note), nil
		}

		text := strings.TrimSpace(fmt.Sprintf("%v", note))
		if note == nil {
			text = ""
		}
		if err := stack.SetNote(ws.Name, rec.Name, text); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if text == "" {
			return mcp.NewToolResultText(fmt.Sprintf("Cleared the note on %q.", rec.Name)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("%s: %s", rec.Name, text)), nil
	})
}

func registerStackRemoveTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_rm",
		mcp.WithDescription("Tear down a feature stack of THIS workspace: stop its daemon, remove its worktrees, release its ports, delete its record, and delete its stack root. Refuses a worktree with uncommitted changes unless force is set. This workspace may declare lifecycle HOOKS that de-provision external state on stack.destroy; they fire before anything is removed, while the ports and record can still be read. A hook failure does NOT block the teardown, so it means the external cleanup probably did not happen — and it cannot be retried afterwards, because removing the stack deletes the record its ${self...} references resolve against. devstack prints the resolved URLs at the point of failure; pass those on instead of reporting a clean teardown."),
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

		// Before anything is taken away, while the record, worktrees and ports
		// are all still readable — a teardown hook de-provisions what create
		// provisioned, and cannot do that once the allocation is gone.
		var hookOut strings.Builder
		hookErr := hooks.Fire(ws, name, config.EventStackDestroy, nil, &hookOut)

		res, err := stack.Remove(ws, name, force)

		var sb strings.Builder
		appendHookOutput(&sb, config.EventStackDestroy, hookOut.String(), hookErr)
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
		mcp.WithDescription("Bring a feature stack up: mark it (and its base workspace) active, fold its <base>:<service>:<stack> resources into the one host Tilt daemon, then enable and trigger them so its services actually start (registering a resource does not run it). Reports which services it started; they are starting, not started, so confirm with status before drawing conclusions. Mirrors 'devstack stack up'. This is the remedy when another tool reports the stack is not up: status, process_logs, restart, start, stop and configure all refuse an inactive stack rather than falling through to base. There is no per-stack daemon — its services run on their own ports inside the host daemon. Returns the stack's allocated service links and the daemon status. This workspace may declare lifecycle HOOKS: shell commands devstack runs automatically on this action, which can change state outside this machine. They fire on their own and their output is included in the result. A hook failure is returned as an error and means the action succeeded but the stack is NOT fully provisioned — do not report success; retry with the hooks tool (action=\"run\"). Call the hooks tool with action=\"list\" first if you do not know what this workspace fires. "+serviceLinksDesc),
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

		tiltClient := tilt.NewClient("localhost", workspace.HostTiltPort)
		for _, note := range hostdaemon.SyncAndReload(tiltClient) {
			daemonMsg += "\n" + note
		}
		started, err := stack.StartServices(tiltClient, ws.Name, rec)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(started) == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("stack %q has no services in the host daemon, so nothing was started — recreate it with stack_rm then stack_create", rec.Name)), nil
		}

		var hookOut strings.Builder
		hookErr := hooks.Fire(ws, rec.Name, config.EventStackUp, started, &hookOut)

		var sb strings.Builder
		fmt.Fprintf(&sb, "Stack %q started: %s. Its services run in the host daemon on :%d as %s:<service>:%s.\n",
			rec.Name, strings.Join(started, ", "), workspace.HostTiltPort, ws.Name, rec.Name)
		if daemonMsg != "" {
			fmt.Fprintf(&sb, "%s\n", daemonMsg)
		}
		for _, k := range sortedPortKeys(rec.Ports) {
			fmt.Fprintf(&sb, "  %-24s http://localhost:%d\n", k, rec.Ports[k])
		}
		appendHookOutput(&sb, config.EventStackUp, hookOut.String(), hookErr)
		if hookErr != nil {
			return mcp.NewToolResultError(sb.String()), nil
		}
		fmt.Fprintf(&sb, "\nThey are starting, not yet started — check with status stack=%q before concluding anything about them.\n", rec.Name)
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackDownTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_down",
		mcp.WithDescription("Stop a feature stack's services in the host daemon: mark it inactive (nothing of it runs any more) and regenerate the host Tiltfile so the running daemon drops its <base>:<service>:<stack> resources. Mirrors 'devstack stack down'. Leaves the stack's worktrees and record intact (remove them with stack_rm). This workspace may declare lifecycle HOOKS on stack.down; they fire on their own and a failure does not block the stack coming down, so it means the external cleanup probably did not happen. Retry with the hooks tool (action=\"run\", event=\"stack.down\")."),
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

		var hookOut strings.Builder
		hookErr := hooks.Fire(ws, rec.Name, config.EventStackDown, nil, &hookOut)

		if err := stack.SetActive(ws.Name, rec.Name, false); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := hostdaemon.Regenerate(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to regenerate host Tiltfile: %v", err)), nil
		}

		var sb strings.Builder
		appendHookOutput(&sb, config.EventStackDown, hookOut.String(), hookErr)
		return mcp.NewToolResultText(sb.String() + fmt.Sprintf(
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

// appendHookOutput folds a lifecycle event's hook output into a tool result. An
// agent that cannot see a hook ran, or failed, has no way to tell a stack that
// was provisioned from one that only looks provisioned.
func appendHookOutput(sb *strings.Builder, event, output string, err error) {
	if strings.TrimSpace(output) == "" && err == nil {
		return
	}
	fmt.Fprintf(sb, "\nHooks (%s):\n", event)
	if strings.TrimSpace(output) != "" {
		for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
			fmt.Fprintf(sb, "  %s\n", line)
		}
	}
	if err != nil {
		fmt.Fprintf(sb, "  FAILED: %v\n", err)
		if !config.IsTeardownEvent(event) {
			fmt.Fprintf(sb, "  The lifecycle action itself succeeded, but setup hooks did not finish — this is NOT fully provisioned.\n")
			fmt.Fprintf(sb, "  Fix the hook, then re-run just the hooks with the 'hooks' tool (action=run, event=%s).\n", event)
		}
	}
}
