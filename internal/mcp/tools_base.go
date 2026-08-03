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
		mcp.WithDescription("Inspect or refresh the replica base runs from. "+baseTermDesc+
			"action=\"path\" reads only: it prints the replica root, or one service's replica worktree, which is the directory that service's base copy runs out of — the directory to read when you need the code base is actually executing. "+
			"action=\"sync\" is the one that changes something: it fetches each service and moves its replica worktree to that service's default branch tip, then re-materialises the machine-local gitignored config copied from the checkout, and reports each service's before and after short SHA. "+
			"This is how an edit made in a checkout reaches base: put it on the default branch, then sync. A fetch that fails is a warning, not an error — being offline leaves that worktree on the ref it already has and base keeps running. "+
			"sync moves the code base runs, and nothing else: it does not restart anything, so every already-running copy keeps serving the OLD code until it is restarted (restart tool, stack=\"base\"). "+
			"Errors when no replica has been built yet — 'devstack workspace up' builds it. Mirrors 'devstack base path' and 'devstack base sync'."),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("\"path\" — print the replica root, or one service's worktree (read only, changes nothing). \"sync\" — move every service's worktree to its default branch tip and refresh its machine-local config (this one writes).")),
		mcp.WithString("service",
			mcp.Description("Exact service name, for example 'api-service'. Only meaningful with action=\"path\", where it prints that service's replica worktree instead of the replica root; ignored by \"sync\", which syncs every service.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved, so there is no replica to inspect"), nil
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
		return mcp.NewToolResultError(fmt.Sprintf("service %q is not in workspace %q; services: %s", service, ws.Name, strings.Join(names, ", ")))
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
		sb.WriteString("Nothing moved — every service was already at its default branch tip.\n")
		return mcp.NewToolResultText(sb.String())
	}
	fmt.Fprintf(&sb, "%d service(s) moved. Nothing was restarted: each running copy still serves the code it started with — restart it (stack=\"base\") before concluding anything about the new tip.\n", moved)
	return mcp.NewToolResultText(sb.String())
}
