package mcp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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
const stackShortNameDesc = "Feature stack SHORT name, for example 'import-review'. This is not the '<base>--<name>' full identity that stack_list and telemetry print. " +
	baseTermDesc +
	"So no stack is ever called \"base\": omit this parameter instead of passing \"base\". " +
	"The telemetry tools accept \"base\" as a synonym for omitting it. The tools that start, stop or restart a service require it, and there \"base\" is not a synonym for omitting it: omitting it means the instance is read from the working directory, or the call fails. The stack tools take stack names only, and stack_rm rejects \"base\"."

// baseTermDesc is the one definition of "base". It is shared rather than
// restated because the restatements disagreed: the same word was used for a
// checkout, for a workspace, for a stack, and for the absence of a stack, and
// the disagreeing paragraph was pasted into eight parameter descriptions.
const baseTermDesc = "\"base\" is this workspace running without any stack. It does not run from the user's checkouts. It runs from a managed replica of the workspace — one git worktree per service, detached at that service's default branch tip, under a .devstack-base sibling — and the checkout is the template that replica is built from. base is not itself a stack. "

// serviceLinksDesc defines the "service links" these tools return: one
// http://localhost:<port> URL per port the stack allocated.
const serviceLinksDesc = "A service link is 'service/portKey=http://localhost:<port>'. It is the localhost URL of one port that this stack allocated for one of its overlay services. " +
	"The portKey is a key from the manifest ports of that service, for example 'http'. These ports belong to the stack. Reach its copies here, not on the ports of base."

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
		mcp.WithDescription("Create a feature stack overlaying THIS workspace (the base). A stack instantiates only the services it changes plus the services that call them, each in its own git worktree on a dynamically allocated port; every other service resolves to base's copy. Use this for the request 'I need a stack to work on X in services A and B'. The base workspace must be running — a stack reuses its services. Returns the overlay set with reasons, worktree paths, service links, and any warnings (dirty base checkout, base daemon not running). This workspace may declare lifecycle HOOKS: shell commands devstack runs automatically on this action, which can change state outside this machine (registering callback URLs, provisioning resources). They fire on their own and their output is included below. A hook failure is returned as an error and means the stack exists but is NOT fully provisioned — do not report success; see the hooks tool. Run the hooks tool with action=\"list\" first if you do not know what this workspace fires. "+serviceLinksDesc),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Short stack name (for example 'import-review'). The full stack identity becomes '<base>--<name>'; every stack parameter across these tools takes the short name.")),
		mcp.WithString("repos", mcp.Required(),
			mcp.Description("Comma-separated exact service names this stack changes (for example 'frontend,backend'). Services that call these are pulled into the overlay automatically.")),
		mcp.WithString("note",
			mcp.Description("What this stack is for, in the author's words — a ticket URL, an issue key, a sentence. devstack never derives this: the branch says what changed, the note says why. Shown by stack_list. Optional, and editable later with the CLI: devstack stack note <name> \"...\"")),
		mcp.WithString("branch",
			mcp.Description("Git branch for the changed repos' worktrees. Created if absent, attached to if it already exists. Defaults to the stack name.")),
		mcp.WithString("from",
			mcp.Description("Git ref the worktrees are cut from, for example 'origin/release-2' or a commit SHA. Defaults to each repo's default branch as origin has it — never to whatever the user's checkout happens to have checked out, which is a template that may hold parked work. Applies only where the branch is created: an existing branch is attached to with the history it already has.")),
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
			From:   strings.TrimSpace(request.GetString("from", "")),
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
			branchNote := "detached at " + wt.Ref
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
		mcp.WithDescription("List the feature stacks of THIS workspace: which services each one overlays (runs its own copy of), its branch, its env, the note saying what it is for, its allocated links, and its base, status (up = its resources are folded into the host daemon; down = worktrees and record exist but nothing of it is registered, so tools that act on running services error \"not up\" for it), the base daemon port their services run in while up, and allocated service links. A stack that is up can still have copies that are not running: \"up\" is about the stack, and each copy has its own state (running, starting, building, erroring, stopped, disabled, down, unknown) — read them with status. The STACK column prints each stack's full identity '<base>--<name>' (the form telemetry and daemon resources use); every stack parameter across these tools takes the short '<name>' half. "+serviceLinksDesc),
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
			if n := len(s.Log); n > 0 {
				fmt.Fprintf(&sb, "  latest:   %s  %s\n", s.Log[n-1].At.Format("2006-01-02"), s.Log[n-1].Text)
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
		mcp.WithDescription("Read, set or add to what a feature stack is for and where it got to. devstack derives everything else about a stack — which services it overlays, its branch, its age — but not why it exists or how far it got, and a week later those are the parts nobody can reconstruct. "+
			"'note' is the standing purpose and replaces what is there. 'append' adds one dated entry to a log of at most "+strconv.Itoa(stack.NoteLogEntries)+" entries. Pass neither to read both. Mirrors 'devstack stack note'."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(stackShortNameDesc)),
		mcp.WithString("note",
			mcp.Description("What the stack is for, replacing the current purpose. Omit to read instead of write; pass \"\" to clear the purpose and its entries.")),
		mcp.WithString("append",
			mcp.Description("One line on where the work got to, appended with today's date. "+
				"Append when the answer to \"what would the next person need to know?\" changed: a decision taken, a blocker hit, a piece verified working, work parked mid-way. "+
				"Do NOT append to narrate: not per file edited, per command run, per test executed, and never to say what you are about to do. If in doubt, do not append — a session that ends with no entry is normal and correct. "+
				"Only the last "+strconv.Itoa(stack.NoteLogEntries)+" entries are kept, so an entry per step deletes the ones worth reading. Maximum "+strconv.Itoa(stack.NoteEntryMax)+" characters, and repeating the last entry is a no-op.")),
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

		note, setting := request.GetArguments()["note"]
		add := strings.TrimSpace(request.GetString("append", ""))
		if setting && add != "" {
			return mcp.NewToolResultError("pass either note (replaces the purpose) or append (adds an entry), not both"), nil
		}

		if add != "" {
			appended, entry, err := stack.AppendNote(ws.Name, rec.Name, add)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if !appended {
				return mcp.NewToolResultText(fmt.Sprintf("Not appended: the last entry on %q already says that.", rec.Name)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("%s: %s", rec.Name, entry.Text)), nil
		}

		if !setting {
			if rec.Note == "" && len(rec.Log) == 0 {
				return mcp.NewToolResultText(fmt.Sprintf("Stack %q has no note. Set one so the next reader knows what it is for.", rec.Name)), nil
			}
			var sb strings.Builder
			if rec.Note != "" {
				fmt.Fprintf(&sb, "%s\n", rec.Note)
			}
			for _, e := range rec.Log {
				fmt.Fprintf(&sb, "  %s  %s\n", e.At.Format("2006-01-02"), e.Text)
			}
			return mcp.NewToolResultText(strings.TrimRight(sb.String(), "\n")), nil
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

		// A refusal must come before the hooks, not after. A removal that
		// de-provisions external state and then refuses leaves the stack alive
		// and already de-provisioned, and the next attempt fires the hooks again.
		if err := stack.CheckRemovable(ws, name, force); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

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
		mcp.WithDescription("Bring a feature stack up: mark it up (and its base workspace active), fold its <base>:<service>:<stack> resources into the one host Tilt daemon, then enable and trigger them so its services start (registering a resource does not run it). Reports which services it started; they are starting, not started, so confirm with status before drawing conclusions. Mirrors 'devstack stack up'. This is the remedy when another tool reports the stack is not up: status, process_logs, restart, start, stop and configure all refuse a stack that is down rather than falling through to base. There is no per-stack daemon — its services run on their own ports inside the host daemon. Returns the stack's allocated service links and the daemon status. This workspace may declare lifecycle HOOKS: shell commands devstack runs automatically on this action, which can change state outside this machine. They fire on their own and their output is included in the result. A hook failure is returned as an error and means the action succeeded but the stack is NOT fully provisioned — do not report success; retry with the hooks tool (action=\"run\"). Call the hooks tool with action=\"list\" first if you do not know what this workspace fires. "+serviceLinksDesc),
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
		_, genWarnings, err := hostdaemon.Regenerate()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to regenerate host Tiltfile: %v", err)), nil
		}
		daemonMsg, err := hostdaemon.EnsureDaemon()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		for _, w := range genWarnings {
			daemonMsg += "\nWARNING: " + w
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
		mcp.WithDescription("Stop a feature stack's services in the host daemon: mark it down (nothing of it runs any more) and regenerate the host Tiltfile so the running daemon drops its <base>:<service>:<stack> resources. Mirrors 'devstack stack down'. Leaves the stack's worktrees and record intact (remove them with stack_rm). This workspace may declare lifecycle HOOKS on stack.down; they fire on their own and a failure does not block the stack coming down, so it means the external cleanup probably did not happen. Retry with the hooks tool (action=\"run\", event=\"stack.down\")."),
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
		_, genWarnings, err := hostdaemon.Regenerate()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to regenerate host Tiltfile: %v", err)), nil
		}

		var sb strings.Builder
		for _, w := range genWarnings {
			fmt.Fprintf(&sb, "WARNING: %s\n", w)
		}
		appendHookOutput(&sb, config.EventStackDown, hookOut.String(), hookErr)
		return mcp.NewToolResultText(sb.String() + fmt.Sprintf(
			"Stack %q is now down; the host daemon will drop its resources. Worktrees and record kept (remove with stack_rm %s).\n"+
				"While it is down, status/process_logs/restart/stop/configure targeting it error \"not up\" instead of falling through to base; "+
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
