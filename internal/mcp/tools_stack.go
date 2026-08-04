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
	"So no stack is ever called \"base\". On this tool, omit this parameter instead of passing \"base\". " +
	"The telemetry tools accept \"base\" as a synonym for an omitted parameter. " +
	"The tools that start, stop or restart a service require the parameter. There, \"base\" is not a synonym for an omitted parameter. " +
	"If you omit it there, devstack reads the copy from the working directory, or the call fails. " +
	"The stack tools take stack names only, and stack_rm rejects \"base\"."

// baseTermDesc is the one definition of "base". It is shared rather than
// restated because the restatements disagreed: the same word was used for a
// checkout, for a workspace, for a stack, and for the absence of a stack, and
// the disagreeing paragraph was pasted into eight parameter descriptions.
const baseTermDesc = "\"base\" is this workspace with no stack. base does not run from the user's checkouts. It runs from a managed replica of the workspace. " +
	"That replica holds one git worktree per service, detached at that service's default branch tip, under a .devstack-base sibling. " +
	"The checkout is the template that devstack builds the replica from. base is not a stack. "

// serviceLinksDesc defines the "service links" these tools return: one
// http://localhost:<port> URL per port the stack allocated.
const serviceLinksDesc = "A service link is 'service/portKey=http://localhost:<port>'. It is the localhost URL of one port that this stack allocated for one of its overlay services. " +
	"The portKey is a key from the manifest ports of that service, for example 'http'. These ports belong to the stack. Reach its copies here, not on the ports of base."

func registerStackTools(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	registerStackCreateTool(mcpServer, ws)
	registerStackAddTool(mcpServer, ws)
	registerStackListTool(mcpServer, ws)
	registerStackNoteTool(mcpServer, ws)
	registerStackRemoveTool(mcpServer, ws)
	registerStackUpTool(mcpServer, ws)
	registerStackDownTool(mcpServer, ws)
}

func registerStackCreateTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_create",
		mcp.WithDescription("Create a feature stack that overlays THIS workspace (the base). A stack runs its own copy of only the services it changes, plus the services that call them. Each one runs in its own git worktree, on a port devstack allocates. Every other service resolves to base's copy. Use this tool for the request 'I need a stack to work on X in services A and B'.\n"+
			"devstack cuts each worktree from that repo's DEFAULT BRANCH as origin has it, and not from whatever the user's checkout has checked out. So a stack starts from what the team shipped, and half-finished work parked in a checkout is not dragged into it. To override this, use the 'from' parameter.\n"+
			"Creation does not start the stack. A new stack is down, and stack_up is what runs it. Base does not have to run for you to create one. A base that is down is returned as a warning, and not as an error. A stack does reuse base's copies for every service it does not overlay, so bring base up before you use the stack.\n"+
			"The tool returns the overlay set with a reason for each member, the worktree paths, the ref each worktree was cut from, the service links, and any warnings. A warning names a checkout that holds work the stack does not have, or a base daemon that does not run.\n"+
			"This workspace can declare lifecycle HOOKS: shell commands that devstack runs on its own for this action. They can change state outside this machine, for example register a callback URL or provision a resource. They fire on their own, and their output is in the result below. A hook failure is returned as an error, and it means that the stack exists and is NOT fully provisioned. Do not report success. See the hooks tool. If you do not know what this workspace fires, run the hooks tool with action=\"list\" first. "+serviceLinksDesc),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Short stack name (for example 'import-review'). The full stack identity becomes '<base>--<name>'. Every stack parameter across these tools takes the short name.")),
		mcp.WithString("repos", mcp.Required(),
			mcp.Description("Comma-separated exact service OR group names that this stack changes (for example 'frontend,backend' or 'core'). A group expands to its members. devstack pulls the services that call any of them into the overlay. A group reaches only as far as the overlay does. A member that devstack does not pull in keeps serving from base, and stack_list reports the shortfall.")),
		mcp.WithString("note",
			mcp.Description("What this stack is for, in the author's words: a ticket URL, an issue key, or a sentence. devstack never derives this: the branch says what changed, and the note says why. stack_list shows it. It is optional, and you can edit it later with the CLI: devstack stack note <name> \"...\"")),
		mcp.WithString("branch",
			mcp.Description("Git branch for the worktrees of the changed repos. devstack creates it where it is absent, and attaches to it where it already exists. The default is the stack name.")),
		mcp.WithString("from",
			mcp.Description("The git ref that devstack cuts the worktrees from, for example 'origin/release-2' or a commit SHA. The default is each repo's default branch, as origin has it. The default is never whatever the user's checkout has checked out, because that checkout is a template and it can hold parked work. This applies only where devstack creates the branch. Where the branch already exists, the worktree attaches to it with the history it already has.")),
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

		fmt.Fprintf(&sb, "\nThe stack is created and down. Nothing of it runs yet. To bring it up, use the stack_up tool (name=%q), or run in a shell: devstack stack up %s\n", name, name)
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackAddTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_add",
		mcp.WithDescription("Add services to a feature stack of THIS workspace that already exists. Use this tool for 'the stack needs service C as well'. The alternative is stack_rm plus stack_create, and that throws away the stack's branches, its worktrees, and any uncommitted work in them.\n"+
			"Each named service gets its own git worktree on the stack's existing branch, and its own allocated port. devstack pulls in the services that call it, exactly as stack_create pulls them in. Everything already in the stack is left alone: the same worktrees, the same branch tips, and the same ports its copies already serve on.\n"+
			"The stack stays as up, or as down, as it was. If it is up, the added copies become resources in the host daemon, and devstack does NOT start them. Start them with the start tool, or report the command to the user. A service already in the stack is reported as already present, and it is not an error. If every name you give is already there, the call fails, because there is nothing to add.\n"+
			"This workspace can declare lifecycle HOOKS: shell commands that devstack runs on its own. Here they are scoped to the ADDED services only, so devstack re-provisions nothing that is already in the stack. stack.create fires for them, and stack.up fires too when the stack is up. Their output is in the result below. A hook failure is returned as an error, and it means that the services were added and are NOT fully provisioned. Do not report success. See the hooks tool. "+serviceLinksDesc),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(stackShortNameDesc)),
		mcp.WithString("services", mcp.Required(),
			mcp.Description("Comma-separated exact service OR group names to add (for example 'billing' or 'core'). A group expands to its members, and devstack records the group on the stack. devstack pulls the services that call any of them into the overlay.")),
		mcp.WithString("from",
			mcp.Description("The git ref that devstack cuts the NEW worktrees from, for example 'origin/release-2' or a commit SHA. The default is each repo's default branch, as origin has it. This applies only where the stack's branch does not yet exist in that repo. Where it does exist, the worktree attaches to it with the history it already has. It never affects the worktrees the stack already has.")),
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
		var members []string
		for _, s := range strings.Split(request.GetString("services", ""), ",") {
			if s = strings.TrimSpace(s); s != "" {
				members = append(members, s)
			}
		}
		if len(members) == 0 {
			return mcp.NewToolResultError("services must name at least one service or group to add"), nil
		}

		res, err := stack.Add(stack.AddInput{
			Base:    ws,
			Name:    name,
			Members: members,
			From:    strings.TrimSpace(request.GetString("from", "")),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		added := make([]string, 0, len(res.Added))
		for _, m := range res.Added {
			added = append(added, m.Service)
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Added %s to stack %q (branch %s).\n", strings.Join(added, ", "), res.StackName, res.Branch)
		for _, m := range res.Added {
			reason := "calls an added service"
			if m.Reason == "changed" {
				reason = "added"
			}
			fmt.Fprintf(&sb, "  %-16s %s\n", m.Service, reason)
		}
		sb.WriteString("\nNew worktrees:\n")
		for _, wt := range res.Worktrees {
			branchNote := "detached at " + wt.Ref
			if wt.Branch != "" {
				branchNote = "branch " + wt.Branch
			}
			fmt.Fprintf(&sb, "  %-16s %s (%s)\n", wt.Service, wt.Path, branchNote)
		}
		if len(res.Ports) > 0 {
			sb.WriteString("\nNewly allocated ports (service/portKey). The existing ports of the stack do not change:\n")
			for _, k := range sortedPortKeys(res.Ports) {
				fmt.Fprintf(&sb, "  %-24s http://localhost:%d\n", k, res.Ports[k])
			}
		}
		if len(res.AlreadyPresent) > 0 {
			fmt.Fprintf(&sb, "\nAlready in the stack, left alone: %s\n", strings.Join(res.AlreadyPresent, ", "))
		}
		fmt.Fprintf(&sb, "\nOverlay is now: %s\n", strings.Join(res.Overlay, ", "))
		for _, w := range res.Warnings {
			fmt.Fprintf(&sb, "WARNING: %s\n", w)
		}

		var createOut strings.Builder
		createErr := hooks.Fire(ws, name, config.EventStackCreate, added, &createOut)
		appendHookOutput(&sb, config.EventStackCreate, createOut.String(), createErr)
		if createErr != nil {
			return mcp.NewToolResultError(sb.String()), nil
		}

		if !res.Active {
			fmt.Fprintf(&sb, "\nStack %q is down, and this call did not change that. To bring it up, use stack_up.\n", res.StackName)
			return mcp.NewToolResultText(sb.String()), nil
		}

		// The stack stays up: regenerating adds the new resources and leaves the
		// blocks of the copies already running untouched, so nothing serving is
		// stopped or restarted here.
		_, genWarnings, err := hostdaemon.Regenerate()
		if err != nil {
			return mcp.NewToolResultError(sb.String() + fmt.Sprintf("\nfailed to regenerate host Tiltfile: %v", err)), nil
		}
		for _, w := range genWarnings {
			fmt.Fprintf(&sb, "WARNING: %s\n", w)
		}
		for _, note := range hostdaemon.SyncAndReload(tilt.NewClient("localhost", workspace.HostTiltPort)) {
			fmt.Fprintf(&sb, "%s\n", note)
		}

		var upOut strings.Builder
		upErr := hooks.Fire(ws, name, config.EventStackUp, added, &upOut)
		appendHookOutput(&sb, config.EventStackUp, upOut.String(), upErr)
		if upErr != nil {
			return mcp.NewToolResultError(sb.String()), nil
		}

		fmt.Fprintf(&sb, "\nThe stack was already up, and it still is. devstack stopped and restarted nothing that it ran. The added copies are registered, and they are NOT started. Start each one with the start tool (stack=%q), or leave them stopped.\n", name)
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackListTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_list",
		mcp.WithDescription("List the feature stacks of THIS workspace. For each stack it shows:\n"+
			"  overlays  the services this stack runs its own copy of.\n"+
			"  branch    the stack's git branch.\n"+
			"  env       the config env the stack is pointed at.\n"+
			"  note      what the stack is for.\n"+
			"  latest    the newest dated entry of the stack's note log, written by stack_note. It says where the work got to.\n"+
			"  covers    how much of each group the stack was cut to cover.\n"+
			"  links     the stack's allocated service links.\n"+
			"It also shows the stack's base, its status, and the port of the base daemon its services run in while it is up.\n"+
			"A status of up means that devstack folded the stack's resources into the host daemon. A status of down means that the worktrees and the record exist, and that nothing of the stack is registered. So the tools that act on running services error \"not up\" for it.\n"+
			"A stack that is up can still hold copies that do not run. \"up\" is about the stack, and each copy has its own state: running, starting, building, erroring, stopped, disabled, down, or unknown. Read those states with status.\n"+
			"Telemetry and daemon resources name a stack by its full identity '<base>--<name>'. This tool prints the short '<name>' half, and every stack parameter across these tools takes that short half.\n"+
			"A 'covers group ...' line appears for each group the stack was cut to cover. 'covers group core (3/4 — importer serves from base)' means that the stack overlays three of that group's four members. The fourth is base's copy, which everybody shares. A group action against the stack reaches only the members it overlays. "+serviceLinksDesc),
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
		baseGroups := map[string][]string{}
		if cfg, cerr := config.Load(ws.Path); cerr == nil && cfg != nil {
			baseGroups = cfg.Groups
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
			for _, cov := range stack.CoverageOf(s.Groups, s.Services, baseGroups) {
				fmt.Fprintf(&sb, "  %s\n", cov.Sentence())
			}
			links := make([]string, 0, len(s.Ports))
			for _, k := range sortedPortKeys(s.Ports) {
				links = append(links, fmt.Sprintf("%s=http://localhost:%d", k, s.Ports[k]))
			}
			if len(links) > 0 {
				fmt.Fprintf(&sb, "  links:    %s\n", strings.Join(links, " "))
			}
		}
		sb.WriteString("\noverlays are the services this stack runs its own copy of. It borrows every other service from base.\n")
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackNoteTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_note",
		mcp.WithDescription("Read, set or add to what a feature stack is for, and where it got to. "+
			"devstack derives everything else about a stack: which services it overlays, its branch, and its age. It does not derive why the stack exists, or how far the work got. A week later, those two are the parts nobody can reconstruct. "+
			"'note' is the standing purpose, and it replaces what is there. 'append' adds one dated entry to a log of at most "+strconv.Itoa(stack.NoteLogEntries)+" entries. Pass neither to read both. This tool mirrors 'devstack stack note'."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(stackShortNameDesc)),
		mcp.WithString("note",
			mcp.Description("What the stack is for. It replaces the current purpose. To read instead of write, omit it. To clear the purpose and its entries, pass \"\".")),
		mcp.WithString("append",
			mcp.Description("One line on where the work got to. devstack appends it with today's date. "+
				"Append when the answer to \"what will the next person need to know?\" changed: a decision taken, a blocker hit, a piece that you saw work, or work parked mid-way. "+
				"Do NOT append to narrate. Not per file edited, not per command run, not per test run, and never to say what you are about to do. If in doubt, do not append. A session that ends with no entry is normal and correct. "+
				"Only the last "+strconv.Itoa(stack.NoteLogEntries)+" entries are kept, so an entry per step deletes the ones worth reading. The maximum is "+strconv.Itoa(stack.NoteEntryMax)+" characters. A repeat of the last entry does nothing.")),
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
		mcp.WithDescription("Tear down a feature stack of THIS workspace. devstack stops its daemon, removes its worktrees, releases its ports, deletes its record, and deletes its stack root. "+
			"It refuses a worktree that has uncommitted changes, until you set force. "+
			"This workspace can declare lifecycle HOOKS that de-provision external state on stack.destroy. They fire before devstack removes anything, while the ports and the record can still be read. "+
			"A hook failure does NOT block the teardown. So it means that the external cleanup probably did not happen. You can not retry it afterwards, because the removal deletes the record that its ${self...} references resolve against. "+
			"devstack prints the resolved URLs at the point of failure. Pass those on, rather than report a clean teardown."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(stackShortNameDesc)),
		mcp.WithBoolean("force",
			mcp.Description("Remove the worktrees even where they have uncommitted changes. CAUTION: this destroys uncommitted work. The default is false.")),
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
		mcp.WithDescription("Bring a feature stack up. devstack marks the stack up, marks its base workspace active, and folds its <base>:<service>:<stack> resources into the one host Tilt daemon. It then enables and triggers them, so that the stack's services start. A resource that is only registered does not run.\n"+
			"The tool reports which services it started. They are starting, and not started, so read status before you draw a conclusion. This tool mirrors 'devstack stack up'.\n"+
			"This is the remedy when another tool reports that the stack is not up. status, process_logs, restart, start, stop and configure all refuse a stack that is down, rather than fall through to base.\n"+
			"There is no daemon per stack. A stack's services run on their own ports inside the host daemon. The tool returns the stack's allocated service links and the daemon status.\n"+
			"This workspace can declare lifecycle HOOKS: shell commands that devstack runs on its own for this action. They can change state outside this machine. They fire on their own, and their output is in the result. A hook failure is returned as an error, and it means that the action succeeded and the stack is NOT fully provisioned. Do not report success. Retry with the hooks tool (action=\"run\"). If you do not know what this workspace fires, call the hooks tool with action=\"list\" first. "+serviceLinksDesc),
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
			return mcp.NewToolResultError(fmt.Sprintf("can not mark the base workspace active: %v", err)), nil
		}
		if err := stack.SetActive(ws.Name, rec.Name, true); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		_, genWarnings, err := hostdaemon.Regenerate()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("can not generate the host Tiltfile again: %v", err)), nil
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
		fmt.Fprintf(&sb, "\nThey are starting, and not yet started. Read status stack=%q before you conclude anything about them.\n", rec.Name)
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackDownTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_down",
		mcp.WithDescription("Stop a feature stack's services in the host daemon. devstack marks the stack down, so nothing of it runs any more. It then regenerates the host Tiltfile, so that the running daemon drops the stack's <base>:<service>:<stack> resources. This tool mirrors 'devstack stack down'. "+
			"It leaves the stack's worktrees and its record intact. To remove those, use stack_rm. "+
			"This workspace can declare lifecycle HOOKS on stack.down. They fire on their own. A failure does not block the stack from coming down, so it means that the external cleanup probably did not happen. Retry with the hooks tool (action=\"run\", event=\"stack.down\")."),
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
			return mcp.NewToolResultError(fmt.Sprintf("can not generate the host Tiltfile again: %v", err)), nil
		}

		var sb strings.Builder
		for _, w := range genWarnings {
			fmt.Fprintf(&sb, "WARNING: %s\n", w)
		}
		appendHookOutput(&sb, config.EventStackDown, hookOut.String(), hookErr)
		return mcp.NewToolResultText(sb.String() + fmt.Sprintf(
			"Stack %q is now down. The host daemon will drop its resources. devstack keeps the worktrees and the record (remove them with stack_rm %s).\n"+
				"While it is down, status, process_logs, restart, stop and configure against it error \"not up\" instead of falling through to base. "+
				"service_env still reads and writes its worktree config. investigate returns only what the stack emitted while it was up.",
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
