package mcp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/hooks"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

const stackShortNameDesc = "Feature stack SHORT name, for example 'import-review'. It is not the '<workspace>--<name>' full identity that telemetry and the host daemon resource names print. " +
	stackTermDesc +
	"The names \"shared\" and \"base\" are reserved. No stack can have either name. "

const requiredStackNameDesc = stackShortNameDesc +
	"This parameter is REQUIRED. Name a stack, and do not omit it."

const optionalStackNameDesc = stackShortNameDesc +
	"This parameter is OPTIONAL. If you omit it, the tool uses no stack."

const stackTermDesc = "A stack runs the services you name, the members of the groups you name, and the services they need. Each one runs in its own git worktree, on its own ports. " +
	"A shared service is a service in the workspace \"shared:\" list. It runs once for the workspace, and never in a stack. Every stack uses it. The shared set is all the shared services. " +
	"The shared set runs from the replica: one git worktree per repository, detached at the default branch tip. Nothing runs in the user's checkout. "

const serviceLinksDesc = "A service link is 'service/portKey=http://localhost:<port>'. It is the localhost URL of one port that this stack allocated for one of its services. " +
	"The portKey is a key from the manifest ports of that service, for example 'http'. These ports belong to the stack. " +
	"A service link is a localhost URL, so it works on this machine only. If a person on another machine needs an address for the same service, call the urls tool."

func registerStackTools(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	registerStackCreateTool(mcpServer, ws)
	registerStackAddTool(mcpServer, ws)
	registerStackDropTool(mcpServer, ws)
	registerStackListTool(mcpServer, ws)
	registerStackConfigTool(mcpServer, ws)
	registerStackNoteTool(mcpServer, ws)
	registerStackRemoveTool(mcpServer, ws)
	registerStackUpTool(mcpServer, ws)
	registerStackDownTool(mcpServer, ws)
	registerStackExtendTool(mcpServer, ws)
	registerStackResyncTool(mcpServer, ws)
	registerStackAdoptTool(mcpServer, ws)
	registerTaskRunTool(mcpServer, ws)
}

func stackBranchNote(wt stack.WorktreeResult) string {
	if wt.Branch != "" {
		return "branch " + wt.Branch
	}
	if wt.Ref == "" {
		return "detached"
	}
	return "detached at " + wt.Ref
}

func registerStackCreateTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_create",
		mcp.WithDescription("Create a feature stack in THIS workspace. A stack runs only the services you name, the members of the groups you name, and the services they need. Each one runs in its own git worktree, on a port devstack allocates. Use this tool for the request 'I need a stack to work on X in services A and B'.\n"+
			"A TEMPLATE is a named recipe for a stack in the workspace manifest. It gives services, groups and an optional environment. The environment tool lists the templates. Pass template, and repos only for extra services.\n"+
			"A 'needs' edge in devstack.service.yaml pulls a service in. A 'calls' edge does not. devstack never pulls in the services that call a member. If a member calls or references a service that the stack does not run, devstack warns and names stack_add. Decide if the change needs that service. "+
			"A shared service is never in a stack. Every stack uses the one shared set. "+
			"A named service gets the stack's branch. A service that comes in through needs is detached at the default branch tip. To commit in one, name it with stack_add.\n"+
			"devstack cuts each worktree from that repo's DEFAULT BRANCH as origin has it, and not from whatever the user's checkout has checked out. So a stack starts from what the team shipped, and half-finished work in a checkout does not come into the stack. To override this, use the 'from' parameter. To run the default branch with no branch of your own, use detach=true.\n"+
			"Creation does not start the stack. A new stack is down, and stack_up is what runs it.\n"+
			"The stop time starts at create. A stack that is down stays down. stack_up sets a new stop time: DEVSTACK_STOP_AFTER after the up (default 24h). "+
			"stack_extend adds up to 24 hours per call. You can call it again: no limit applies to the total. devstack only stops a stack at its stop time. It never removes one.\n"+
			"The tool returns each member with the reason it is in the stack, the worktree paths, the ref that devstack cut each worktree from, the service links, and any warnings. A warning names a checkout that holds work the stack does not have, or a service the stack does not run.\n"+
			"This workspace can declare lifecycle HOOKS: shell commands that devstack runs on its own for this action. They can change state outside this machine, for example register a callback URL or provision a resource. They fire on their own, and their output is in the result below. A hook failure is returned as an error, and it means that the stack exists and is NOT fully provisioned. Do not report success. See the hooks tool. If you do not know what this workspace fires, run the hooks tool with action=\"list\" first. "+serviceLinksDesc),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Short lowercase stack name, for example the ticket key 'nvxa-9999'. The branch defaults to this name. For a real branch name, pass branch. The full stack identity becomes '<workspace>--<name>'. Every stack parameter across these tools takes the short name. The names \"shared\" and \"base\" are reserved. No stack can have either name.")),
		mcp.WithString("repos",
			mcp.Description("Comma-separated exact service OR group names that this stack changes (for example 'frontend,backend' or 'core'). A group expands to its members. devstack adds the services that they need. A service that they only call does not come in, and devstack warns about it. devstack refuses a shared service. Give repos, template, or both.")),
		mcp.WithString("template",
			mcp.Description("Name of a template from the workspace manifest. Its services and groups become members, with repos added. If the template names an environment, the stack uses it, as env_use with stack does. The environment tool lists the templates.")),
		mcp.WithString("note",
			mcp.Description("What this stack is for, in the author's words: a ticket URL, an issue key, or a sentence. devstack never derives this: the branch says what changed, and the note says why. stack_list shows it. It is optional. To edit it later, call stack_note.")),
		mcp.WithString("branch",
			mcp.Description("Git branch for the worktrees of the changed repos. devstack creates it where it is absent, and attaches to it where it already exists. If the branch exists on origin, devstack fetches it and the stack tracks it. The default is the stack name.")),
		mcp.WithBoolean("detach",
			mcp.Description("If true, devstack cuts every worktree detached at the default branch tip, with no branch. Use it to run the default branch. Do not combine it with branch.")),
		mcp.WithString("from",
			mcp.Description("The git ref that devstack cuts the worktrees from, for example 'origin/release-2' or a commit SHA. The default is each repo's default branch, as origin has it. The default is never whatever the user's checkout has checked out, because that checkout is the source and it can hold parked work. This applies only where devstack creates the branch. Where the branch already exists, the worktree attaches to it with the history it already has.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
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
		template := strings.TrimSpace(request.GetString("template", ""))
		if len(repos) == 0 && template == "" {
			return mcp.NewToolResultError("give repos, template, or both. repos names the services that this stack changes"), nil
		}

		in := stack.CreateInput{
			Note:     request.GetString("note", ""),
			Base:     ws,
			Name:     name,
			Repos:    repos,
			Branch:   strings.TrimSpace(request.GetString("branch", "")),
			Detach:   request.GetBool("detach", false),
			From:     strings.TrimSpace(request.GetString("from", "")),
			Template: template,
		}
		if caller := auth.CallerFrom(ctx); caller.Scope != auth.ScopeOwner {
			in.Owner = caller.Name
		}
		res, err := stack.Create(in)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		bindPane(ctx, ws, name)
		defer herdr.Notify()

		members := make([]string, 0, len(res.Members))
		for _, m := range res.Members {
			members = append(members, m.Service)
		}
		var hookOut strings.Builder
		hookErr := hooks.Fire(ws, name, config.EventStackCreate, members, &hookOut)

		var sb strings.Builder
		fmt.Fprintf(&sb, "Stack %q created (workspace %s).\n", res.StackName, res.BaseName)
		fmt.Fprintf(&sb, "Stack root: %s\n", res.StackRoot)
		if res.Template != "" {
			fmt.Fprintf(&sb, "Template: %s\n", res.Template)
		}
		if res.Env != "" {
			fmt.Fprintf(&sb, "Environment: %s\n", res.Env)
		}
		sb.WriteString("\n")

		sb.WriteString("Members (the services you named, and the services they need):\n")
		for _, m := range res.Members {
			fmt.Fprintf(&sb, "  %-16s %s\n", m.Service, m.Describe())
		}

		sb.WriteString("\nWorktrees:\n")
		for _, wt := range res.Worktrees {
			fmt.Fprintf(&sb, "  %-16s %s (%s)\n", wt.Service, wt.Path, stackBranchNote(wt))
			if wt.Path != wt.RepoPath {
				fmt.Fprintf(&sb, "                   in the worktree of the repository %s at %s\n", wt.Repo, wt.RepoPath)
			}
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
			"Each named service gets its own git worktree on the stack's existing branch, and its own allocated port. devstack also adds the services they need, exactly as stack_create does. It never adds the services that call them. devstack does not change what is already in the stack: the same worktrees, the same branch tips, and the same ports its services already serve on. The one exception: a named member on a detached HEAD moves to the stack's branch, so you can commit in it.\n"+
			"The stack stays as up, or as down, as it was. If it is up, the added services become resources in the host daemon, and devstack does NOT start them. Start them with the start tool, or report the command to the user. If a service is already in the stack on its branch, devstack reports it. That is not an error. If every name you give is already there, the call fails, because there is nothing to add.\n"+
			"This workspace can declare lifecycle HOOKS: shell commands that devstack runs on its own. Here they are scoped to the ADDED services only, so devstack re-provisions nothing that is already in the stack. stack.create fires for them, and stack.up fires too when the stack is up. Their output is in the result below. A hook failure is returned as an error, and it means that the services were added and are NOT fully provisioned. Do not report success. See the hooks tool. "+serviceLinksDesc),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithString("services", mcp.Required(),
			mcp.Description("Comma-separated exact service OR group names to add (for example 'billing' or 'core'). A group expands to its members, and devstack records the group on the stack. devstack adds the services that they need. devstack refuses a shared service.")),
		mcp.WithString("from",
			mcp.Description("The git ref that devstack cuts the NEW worktrees from, for example 'origin/release-2' or a commit SHA. The default is each repo's default branch, as origin has it. This applies only where the stack's branch does not yet exist in that repo. Where it does exist, the worktree attaches to it with the history it already has. It never affects the worktrees the stack already has.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
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
		if len(added) == 0 {
			fmt.Fprintf(&sb, "Nothing was added to stack %q (branch %s).\n", res.StackName, res.Branch)
		} else {
			fmt.Fprintf(&sb, "Added %s to stack %q (branch %s).\n", strings.Join(added, ", "), res.StackName, res.Branch)
		}
		for _, m := range res.Added {
			fmt.Fprintf(&sb, "  %-16s %s\n", m.Service, m.Describe())
		}
		sb.WriteString("\nNew worktrees:\n")
		for _, wt := range res.Worktrees {
			fmt.Fprintf(&sb, "  %-16s %s (%s)\n", wt.Service, wt.Path, stackBranchNote(wt))
			if wt.Path != wt.RepoPath {
				fmt.Fprintf(&sb, "                   in the worktree of the repository %s at %s\n", wt.Repo, wt.RepoPath)
			}
		}
		if len(res.Promoted) > 0 {
			fmt.Fprintf(&sb, "\nPromoted to branch %s: %s. These services were in the stack on a detached HEAD. Their worktrees are on the stack's branch now, so you can commit in them.\n",
				res.Branch, strings.Join(res.Promoted, ", "))
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
		fmt.Fprintf(&sb, "\nMembers are now: %s\n", strings.Join(res.Overlay, ", "))
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
		_, genWarnings, _, err := hostdaemon.RegenerateScope(hostdaemon.ScopeStack(ws.Name, res.Namespace))
		if err != nil {
			return mcp.NewToolResultError(sb.String() + fmt.Sprintf("\nfailed to regenerate host Tiltfile: %v", err)), nil
		}
		for _, w := range genWarnings {
			fmt.Fprintf(&sb, "WARNING: %s\n", w)
		}
		for _, note := range hostdaemon.SyncAndReload(tilt.NewClient("localhost", workspace.HostTiltPort), hostdaemon.ScopeStack(ws.Name, res.Namespace)) {
			fmt.Fprintf(&sb, "%s\n", note)
		}

		var upOut strings.Builder
		upErr := hooks.Fire(ws, name, config.EventStackUp, added, &upOut)
		appendHookOutput(&sb, config.EventStackUp, upOut.String(), upErr)
		if upErr != nil {
			return mcp.NewToolResultError(sb.String()), nil
		}

		fmt.Fprintf(&sb, "\nThe stack was already up, and it still is. devstack stopped and restarted nothing that it ran. devstack registered the added services, and it did NOT start them. Start each one with the start tool (stack=%q), or leave them stopped.\n", name)
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackDropTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_remove",
		mcp.WithDescription("Take services out of a feature stack of THIS workspace, and keep the rest of the stack as it is. Use this tool for 'the stack does not need service C any more'. To delete the whole stack, use stack_rm instead. This tool mirrors 'devstack stack remove'.\n"+
			"devstack removes the named services from the stack record and the stack manifest, and releases only their ports. The other members keep their worktrees, their branches and their ports. If the stack is up, the host daemon drops the removed services.\n"+
			"It refuses to remove a service that another member still needs, and it names that member. Remove both together, or keep it.\n"+
			"devstack keeps the worktrees of the removed services on disk, and the result gives the git command that deletes each one. Never edit stacks.json, ports.json or a stack manifest by hand; use stack_add and stack_remove."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithString("services", mcp.Required(),
			mcp.Description("Comma-separated exact service names to take out of the stack (for example 'billing'). Each one must be in the stack.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		var services []string
		for _, s := range strings.Split(request.GetString("services", ""), ",") {
			if s = strings.TrimSpace(s); s != "" {
				services = append(services, s)
			}
		}
		if len(services) == 0 {
			return mcp.NewToolResultError("services must name at least one service to remove"), nil
		}

		var hookOut strings.Builder
		var hookErr error
		res, err := stack.Drop(stack.DropInput{Base: ws, Name: name, Services: services, Teardown: func(removed []string) {
			hookErr = hooks.Fire(ws, name, config.EventStackDestroy, removed, &hookOut)
		}})

		var sb strings.Builder
		appendHookOutput(&sb, config.EventStackDestroy, hookOut.String(), hookErr)
		if err != nil {
			if sb.Len() > 0 {
				return mcp.NewToolResultError(sb.String() + "\n" + err.Error()), nil
			}
			return mcp.NewToolResultError(err.Error()), nil
		}
		fmt.Fprintf(&sb, "Removed %s from stack %q.\n", strings.Join(res.Removed, ", "), res.StackName)
		if len(res.ReleasedPorts) > 0 {
			fmt.Fprintf(&sb, "Released ports: %s\n", strings.Join(res.ReleasedPorts, ", "))
		}
		fmt.Fprintf(&sb, "Members are now: %s\n", strings.Join(res.Overlay, ", "))
		sb.WriteString("\ndevstack kept these worktrees on disk:\n")
		for _, k := range res.Kept {
			fmt.Fprintf(&sb, "  %-16s %s\n", k.Service, k.Path)
			if k.RemoveCommand != "" {
				fmt.Fprintf(&sb, "                   to delete it: %s\n", k.RemoveCommand)
			} else {
				sb.WriteString("                   a service still in the stack shares this worktree\n")
			}
		}

		if !res.Active {
			fmt.Fprintf(&sb, "\nStack %q is down, and this call did not change that.\n", res.StackName)
			return mcp.NewToolResultText(sb.String()), nil
		}
		_, genWarnings, _, err := hostdaemon.RegenerateScope(hostdaemon.ScopeStack(ws.Name, res.Namespace))
		if err != nil {
			return mcp.NewToolResultError(sb.String() + fmt.Sprintf("\nfailed to regenerate host Tiltfile: %v", err)), nil
		}
		for _, w := range genWarnings {
			fmt.Fprintf(&sb, "WARNING: %s\n", w)
		}
		for _, note := range hostdaemon.SyncAndReload(tilt.NewClient("localhost", workspace.HostTiltPort), hostdaemon.ScopeStack(ws.Name, res.Namespace)) {
			fmt.Fprintf(&sb, "%s\n", note)
		}
		fmt.Fprintf(&sb, "\nThe stack is still up. The host daemon dropped %s, and it left the other members running.\n", strings.Join(res.Removed, ", "))
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackListTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_list",
		mcp.WithDescription("List the feature stacks of THIS workspace. For each stack it shows:\n"+
			"  members   the services this stack runs.\n"+
			"  branch    the stack's git branch.\n"+
			"  env       the config env the stack is pointed at.\n"+
			"  note      the stack's purpose: what it is for.\n"+
			"  latest    the newest dated entry of the stack's note log, written by stack_note. It says where the work got to.\n"+
			"  owner     the agent that created the stack, if an agent did.\n"+
			"  state     when an up stack stops, or when devstack stopped it.\n"+
			"  covers    how much of each group devstack cut the stack to cover.\n"+
			"  links     the stack's allocated service links.\n"+
			"It also shows each stack's status, and the port of the host daemon its services run in while it is up.\n"+
			"A status of up means that devstack folded the stack's resources into the host daemon. A status of down means that the worktrees and the record exist, and that no copy of the stack is in the daemon. So the tools that act on running services error \"not up\" for it.\n"+
			"A stack that is up can still hold services that do not run. \"up\" is about the stack, and each service has its own state: running, starting, building, erroring, stopped, disabled, down, or unknown. Read those states with status.\n"+
			"Telemetry and daemon resources name a stack by its full identity '<workspace>--<name>'. This tool prints the short '<name>' half, and every stack parameter across these tools takes that short half.\n"+
			"A 'covers group ...' line appears for each group that devstack cut the stack to cover. 'covers group core (3/4). importer is not in this stack' means that the stack runs three of that group's four members. A group action against the stack reaches only the members it runs. "+serviceLinksDesc),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
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
			fmt.Fprintf(&sb, "%s (%s, host :%d)\n", short, s.Status, s.BasePort)
			services := "-"
			if len(s.Services) > 0 {
				services = strings.Join(s.Services, ", ")
			}
			fmt.Fprintf(&sb, "  members:  %s\n", services)
			fmt.Fprintf(&sb, "  branch:   %s\n", s.Branch)
			if s.Env != "" {
				fmt.Fprintf(&sb, "  env:      %s\n", s.Env)
			}
			if s.Owner != "" {
				fmt.Fprintf(&sb, "  owner:    %s\n", s.Owner)
			}
			if d := s.Deadline(stack.Now()); d != "" {
				fmt.Fprintf(&sb, "  state:    %s\n", d)
			}
			if s.AdoptedFrom != "" {
				fmt.Fprintf(&sb, "  adopted:  from %s\n", s.AdoptedFrom)
			}
			if s.Template != "" {
				fmt.Fprintf(&sb, "  template: %s\n", s.Template)
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
		sb.WriteString("\nThe members are the services this stack runs. It uses the shared services of the workspace. It runs no other service.\n")
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackNoteTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_note",
		mcp.WithDescription("Read, set or add to what a feature stack is for, and where it got to. "+
			"devstack derives everything else about a stack: which services it runs, its branch, and its age. It does not derive why the stack exists, or how far the work got. A week later, those two are the parts nobody can reconstruct. "+
			"'note' is the standing purpose, and it replaces what is there. 'append' adds one dated entry to a log of at most "+strconv.Itoa(stack.NoteLogEntries)+" entries. Pass neither to read both. This tool mirrors 'devstack stack note'."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
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
		mcp.WithDescription("Tear down a feature stack of THIS workspace. devstack removes its worktrees, releases its ports, deletes its record, and deletes its stack root. "+
			"If the stack is up, devstack first takes its services out of the host daemon. It does not fire stack.down hooks. "+
			"It refuses a worktree that has uncommitted changes, until you set force. "+
			"devstack keeps the branch of the stack, and somebody must decide what happens to it. Before you call this tool, ask the user: merge the branch of this stack, or discard it? NEVER merge it without an answer. After a merge, delete the branch with `git branch -d <branch>`. "+
			"This workspace can declare lifecycle HOOKS that de-provision external state on stack.destroy. They fire before devstack removes anything, while the ports and the record can still be read. "+
			"A hook failure does NOT block the teardown. So it means that the external cleanup probably did not happen. You can not retry it afterwards, because the removal deletes the record that its ${self...} references resolve against. "+
			"devstack prints the resolved URLs at the point of failure. Pass those on, rather than report a clean teardown."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithBoolean("force",
			mcp.Description("Remove the worktrees even where they have uncommitted changes. CAUTION: this destroys uncommitted work. The default is false.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		defer herdr.Notify()
		return removeStack(ws, name, request.GetBool("force", false)), nil
	})
}

func removeStack(ws *workspace.Workspace, name string, force bool) *mcp.CallToolResult {
	if err := stack.CheckRemovable(ws, name, force); err != nil {
		return mcp.NewToolResultError(err.Error())
	}

	rec, err := stack.Resolve(ws.Name, name)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}

	var hookOut strings.Builder
	hookErr := hooks.Fire(ws, name, config.EventStackDestroy, nil, &hookOut)

	det, err := hostdaemon.DetachStack(ws.Name, rec.Name)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}

	res, err := stack.Remove(ws, name, force)

	var sb strings.Builder
	appendHookOutput(&sb, config.EventStackDestroy, hookOut.String(), hookErr)
	for _, w := range det.Warnings {
		fmt.Fprintf(&sb, "WARNING: %s\n", w)
	}
	if det.RegenErr != nil {
		fmt.Fprintf(&sb, "warning: failed to regenerate host Tiltfile: %v\n", det.RegenErr)
	}
	if note := hostdaemon.KeptNote(det.Kept); note != "" {
		fmt.Fprintf(&sb, "%s\n", note)
	}
	if res != nil {
		fmt.Fprintf(&sb, "Removing stack %q (workspace %s)\n", res.Name, res.BaseName)
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
			return mcp.NewToolResultError(sb.String() + "\n" + err.Error())
		}
		return mcp.NewToolResultError(err.Error())
	}
	fmt.Fprintf(&sb, "Stack %q removed.", res.Name)
	return mcp.NewToolResultText(sb.String())
}

func registerStackUpTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_up",
		mcp.WithDescription("Bring a feature stack up. devstack marks the stack up, marks its workspace active, and folds its <workspace>:<service>:<stack> resources into the one host Tilt daemon. It then enables and triggers them, so that the stack's services start. A resource that is only registered does not run.\n"+
			"devstack also builds the replica if it is absent, and starts the shared set. The stack stops DEVSTACK_STOP_AFTER from now (default 24h). To keep it up longer, use stack_extend.\n"+
			"The tool reports which services it started. They are starting, and not started, so read status before you draw a conclusion. This tool mirrors 'devstack stack up'.\n"+
			"devstack checks each worktree against origin: a repo on the stack's branch against that branch, and a detached repo against the default branch. If a worktree is behind, the result names stack_resync. stack_up does not resync.\n"+
			"This is the remedy when another tool reports that the stack is not up. status, process_logs, restart, start, stop and configure all refuse a stack that is down.\n"+
			"There is no daemon per stack. A stack's services run on their own ports inside the host daemon. The tool returns the stack's allocated service links and the daemon status.\n"+
			"This workspace can declare lifecycle HOOKS: shell commands that devstack runs on its own for this action. They can change state outside this machine. They fire on their own, and their output is in the result. A hook failure is returned as an error, and it means that the action succeeded and the stack is NOT fully provisioned. Do not report success. Retry with the hooks tool (action=\"run\"). If you do not know what this workspace fires, call the hooks tool with action=\"list\" first. "+serviceLinksDesc),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
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
			return mcp.NewToolResultError(fmt.Sprintf("can not mark the workspace active: %v", err)), nil
		}
		if err := stack.MarkUp(ws.Name, rec.Name); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		bindPane(ctx, ws, rec.Name)
		defer herdr.Notify()
		var sharedOut strings.Builder
		if !config.HasWorkspaceManifest(replica.Root(ws)) {
			if err := sharedReplicaBuild(ws, &sharedOut); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}
		_, genWarnings, _, err := hostdaemon.RegenerateScope(hostdaemon.ScopeStack(ws.Name, rec.Name))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("can not generate the host Tiltfile again: %v", err)), nil
		}
		daemonMsg, err := hostdaemon.EnsureDaemon()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		for _, w := range append(genWarnings, stack.RecordWarnings(rec)...) {
			daemonMsg += "\nWARNING: " + w
		}
		if err := hostdaemon.StartShared(&sharedOut, ws); err != nil {
			daemonMsg += "\nWARNING: " + err.Error()
		}
		if out := strings.TrimSpace(sharedOut.String()); out != "" {
			daemonMsg += "\n" + out
		}

		tiltClient := tilt.NewClient("localhost", workspace.HostTiltPort)
		for _, note := range hostdaemon.SyncAndReload(tiltClient, hostdaemon.ScopeStack(ws.Name, rec.Name)) {
			daemonMsg += "\n" + note
		}
		started, err := stack.StartServices(tiltClient, ws.Name, rec)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(started) == 0 {
			return mcp.NewToolResultError(hostdaemon.NoServicesReason(tiltClient, rec.Name) + ". Nothing was started"), nil
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
		behind, notes := stack.BehindWarnings(rec, "Run `stack_resync`.")
		for _, w := range behind {
			fmt.Fprintf(&sb, "WARNING: %s\n", w)
		}
		for _, n := range notes {
			fmt.Fprintf(&sb, "note: %s\n", n)
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
		mcp.WithDescription("Stop a feature stack's services in the host daemon. devstack marks the stack down, so nothing of it runs any more. It then regenerates the host Tiltfile, so that the running daemon drops the stack's <workspace>:<service>:<stack> resources. This tool mirrors 'devstack stack down'. "+
			"It leaves the stack's worktrees and its record intact. To remove those, use stack_rm. It does not stop the shared set. "+
			"This workspace can declare lifecycle HOOKS on stack.down. They fire on their own. A failure does not block the stack from coming down, so it means that the external cleanup probably did not happen. Retry with the hooks tool (action=\"run\", event=\"stack.down\")."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		defer herdr.Notify()
		res, _ := stackDown(ws, name, stack.StoppedByUser, time.Time{})
		return res, nil
	})
}

func stackDown(ws *workspace.Workspace, name, by string, dueBy time.Time) (*mcp.CallToolResult, bool) {
	rec, err := stack.Resolve(ws.Name, name)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), true
	}
	if !dueBy.IsZero() && !rec.DueAt(dueBy) {
		return nil, false
	}

	var hookOut strings.Builder
	hookErr := hooks.Fire(ws, rec.Name, config.EventStackDown, nil, &hookOut)

	if dueBy.IsZero() {
		err = stack.MarkDown(ws.Name, rec.Name, by)
	} else {
		var due bool
		due, err = stack.MarkDownIfDue(ws.Name, rec.Name, by, dueBy)
		if err == nil && !due {
			return nil, false
		}
	}
	if err != nil {
		return mcp.NewToolResultError(err.Error()), true
	}
	_, genWarnings, kept, err := hostdaemon.RegenerateScope(hostdaemon.ScopeStack(ws.Name, rec.Name))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("can not generate the host Tiltfile again: %v", err)), true
	}

	var sb strings.Builder
	for _, w := range genWarnings {
		fmt.Fprintf(&sb, "WARNING: %s\n", w)
	}
	if note := hostdaemon.KeptNote(kept); note != "" {
		fmt.Fprintf(&sb, "%s\n", note)
	}
	appendHookOutput(&sb, config.EventStackDown, hookOut.String(), hookErr)
	return mcp.NewToolResultText(sb.String() + fmt.Sprintf(
		"Stack %q is now down. The host daemon will drop its resources. devstack keeps the worktrees and the record (remove them with stack_rm %s).\n"+
			"While it is down, status, process_logs, restart, stop and configure against it error \"not up\". "+
			"service_env still reads and writes its worktree config. investigate returns only what the stack emitted while it was up.",
		rec.Name, rec.Name)), true
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
