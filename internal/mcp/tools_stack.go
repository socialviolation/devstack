package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerStackTools(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	registerStackCreateTool(mcpServer, ws)
	registerStackListTool(mcpServer)
	registerStackRemoveTool(mcpServer)
}

func registerStackCreateTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_create",
		mcp.WithDescription("Create a feature stack overlaying THIS workspace (the base). A stack instantiates only the services it changes plus the services that call them, each in its own git worktree on a dynamically allocated port; every other service resolves to the base stack. Use this for the request 'I need a stack to work on X in services A and B'. The base workspace must be running — a stack reuses its services. Returns the overlay set with reasons, worktree paths, allocated ports/links, and any warnings (dirty base checkout, base daemon not running)."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Short stack name (e.g. 'import-review'). The full stack identity becomes '<base>--<name>'.")),
		mcp.WithString("repos", mcp.Required(),
			mcp.Description("Comma-separated exact service names this stack changes (e.g. 'frontend,backend'). Services that call these are pulled into the overlay automatically.")),
		mcp.WithString("branch",
			mcp.Description("Git branch for the changed repos' worktrees. Created if absent, attached to if it already exists. Defaults to the stack name.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no base workspace resolved for stacks"), nil
		}
		if ws.IsStack() {
			return mcp.NewToolResultError(fmt.Sprintf("%q is itself a stack; a stack can't be the base for another stack", ws.Name)), nil
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

		fmt.Fprintf(&sb, "\nStart it: (cd %s && devstack up)\n", res.StackRoot)
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackListTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("stack_list",
		mcp.WithDescription("List registered feature stacks with their base workspace, daemon port, status (running/starting/stopped), and allocated service links."),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stacks, err := stack.List()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(stacks) == 0 {
			return mcp.NewToolResultText("No stacks registered."), nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%-24s %-14s %-6s %-9s %s\n", "STACK", "BASE", "PORT", "STATUS", "LINKS")
		for _, s := range stacks {
			links := make([]string, 0, len(s.Ports))
			for _, k := range sortedPortKeys(s.Ports) {
				links = append(links, fmt.Sprintf("%s=http://localhost:%d", k, s.Ports[k]))
			}
			linkStr := "-"
			if len(links) > 0 {
				linkStr = strings.Join(links, " ")
			}
			fmt.Fprintf(&sb, "%-24s %-14s %-6d %-9s %s\n", s.Name, s.BaseName, s.Port, s.Status, linkStr)
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStackRemoveTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("stack_rm",
		mcp.WithDescription("Tear down a feature stack: stop its daemon, remove its worktrees, release its ports, deregister it, and delete its stack root. Refuses a worktree with uncommitted changes unless force is set."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Stack name — the short feature name or the full '<base>--<name>' identity.")),
		mcp.WithBoolean("force",
			mcp.Description("Remove worktrees even if they have uncommitted changes. Destroys uncommitted work. Defaults to false.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		force := request.GetBool("force", false)

		res, err := stack.Remove(name, force)

		var sb strings.Builder
		if res != nil {
			fmt.Fprintf(&sb, "Removing stack %q (base %s)\n", res.Name, res.BaseName)
			if res.DaemonPID > 0 {
				fmt.Fprintf(&sb, "  stopped daemon (pid %d)\n", res.DaemonPID)
			}
			for _, p := range res.RemovedWorktrees {
				fmt.Fprintf(&sb, "  removed worktree %s\n", p)
			}
			if res.PortsReleased {
				sb.WriteString("  released allocated ports\n")
			}
			if res.Deregistered {
				fmt.Fprintf(&sb, "  deregistered %q\n", res.Name)
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

func sortedPortKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
