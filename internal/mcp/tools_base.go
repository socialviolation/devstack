package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerBaseTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("base",
		mcp.WithDescription("Inspect or refresh the replica that base runs from. "+baseTermDesc+
			"action=\"path\" reads only. It prints the replica root, or one service's replica worktree. That worktree is the directory that service's base copy runs out of. Read it when you need the code that base runs.\n"+
			"action=\"sync\" is the one that changes something. It fetches each service and moves that service's replica worktree to the default branch tip. It then copies the machine-local gitignored config out of the checkout again. It reports each service's short SHA before and after.\n"+
			"This is how an edit made in a checkout reaches base: put the edit on the default branch, then sync. A fetch that fails is a warning, and not an error. If this machine is offline, that worktree stays on the ref it already has, and base keeps running.\n"+
			"sync moves the code that base runs, and nothing else. It does not restart anything. So every copy that already runs keeps serving the OLD code until somebody restarts it (restart tool, stack=\"base\").\n"+
			"The tool errors when devstack has built no replica yet. 'devstack workspace up' builds one. This tool mirrors 'devstack base path' and 'devstack base sync'."),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("\"path\" prints the replica root, or one service's worktree. It reads only and changes nothing. \"sync\" moves every service's worktree to its default branch tip and refreshes its machine-local config. This action writes.")),
		mcp.WithString("service",
			mcp.Description("Exact service name, for example 'api-service'. It applies only to action=\"path\", where the tool prints that service's replica worktree instead of the replica root. action=\"sync\" ignores it and syncs every service.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so there is no replica to inspect"), nil
		}
		service := strings.TrimSpace(request.GetString("service", ""))
		switch request.GetString("action", "") {
		case "path":
			return baseReplicaPath(ws, service), nil
		case "sync":
			return baseReplicaSync(ws), nil
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use \"path\" or \"sync\"", request.GetString("action", ""))), nil
		}
	})
}

func baseReplicaPath(ws *workspace.Workspace, service string) *mcp.CallToolResult {
	rw, err := replica.Resolve(ws)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	if service == "" {
		return mcp.NewToolResultText(replica.Root(ws))
	}
	svc, ok := rw.Services[service]
	if !ok {
		names := make([]string, 0, len(rw.Services))
		for n := range rw.Services {
			names = append(names, n)
		}
		sort.Strings(names)
		return mcp.NewToolResultError(fmt.Sprintf("service %q is not in workspace %q. Services: %s", service, ws.Name, strings.Join(names, ", ")))
	}
	return mcp.NewToolResultText(svc.RepoPath)
}

func baseReplicaSync(ws *workspace.Workspace) *mcp.CallToolResult {
	res, err := replica.Sync(ws)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}

	moved := 0
	var sb strings.Builder
	fmt.Fprintf(&sb, "Replica for %q: %s\n", ws.Name, res.Root)
	for _, s := range res.Services {
		if s.Before == s.After {
			fmt.Fprintf(&sb, "  %-16s %-24s %s\n", s.Service, s.Ref, s.After)
			continue
		}
		moved++
		fmt.Fprintf(&sb, "  %-16s %-24s %s → %s\n", s.Service, s.Ref, s.Before, s.After)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(&sb, "warning: %s\n", w)
	}
	if moved == 0 {
		sb.WriteString("Nothing moved. Every service was already at its default branch tip.\n")
		return mcp.NewToolResultText(sb.String())
	}
	fmt.Fprintf(&sb, "%d service(s) moved. Nothing was restarted. Each running copy still serves the code it started with. Restart it (stack=\"base\") before you conclude anything about the new tip.\n", moved)
	return mcp.NewToolResultText(sb.String())
}
