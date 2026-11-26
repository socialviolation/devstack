package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

var sharedRestarter = hostdaemon.SharedRestarter

func registerSharedTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("shared",
		mcp.WithDescription("Inspect or refresh the shared services of the workspace. A shared service is a service in the workspace \"shared:\" list. It runs once for the workspace, and never in a stack. Every stack uses it. It runs from the replica: a git worktree detached at the default branch tip.\n"+
			"action=\"status\" reads only. It lists each shared service, its replica commit and its replica worktree.\n"+
			"action=\"path\" reads only. It prints the replica root, or one shared service's replica worktree. Read it when you need the code that a shared service runs.\n"+
			"action=\"sync\" writes. It builds the replica if it is absent. It fetches each repository that holds a shared service, and moves the replica worktree to the default branch tip. It then restarts each shared service that runs and whose commit changed. If a fetch fails, the worktree keeps its commit, and the service keeps running.\n"+
			"This tool mirrors 'devstack shared status', 'devstack shared path' and 'devstack shared sync'."),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("\"status\" lists the shared services. \"path\" prints the replica root, or one shared service's worktree. \"sync\" moves the replica to the default branch tip and restarts the shared services that moved. Only sync writes.")),
		mcp.WithString("service",
			mcp.Description("Exact shared service name. It applies only to action=\"path\", where the tool prints that service's replica worktree instead of the replica root.")),
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
		case "status":
			return sharedStatus(ws), nil
		case "path":
			return sharedReplicaPath(ws, service), nil
		case "sync":
			return sharedReplicaSync(ws), nil
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use \"status\", \"path\" or \"sync\"", request.GetString("action", ""))), nil
		}
	})
}

func sharedStatus(ws *workspace.Workspace) *mcp.CallToolResult {
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	names := template.SharedNames()
	if len(names) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("Workspace %q has no shared services.", ws.Name))
	}
	rw, err := replica.Resolve(ws)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Replica for %q: %s\n", ws.Name, replica.Root(ws))
	for _, n := range names {
		svc, ok := rw.Services[n]
		if !ok {
			fmt.Fprintf(&sb, "  %-16s not in the replica. To build it, use action=\"sync\"\n", n)
			continue
		}
		fmt.Fprintf(&sb, "  %-16s %-24s %s\n", n, gitinfo.Read(svc.RepoPath).Label(), svc.RepoPath)
	}
	return mcp.NewToolResultText(sb.String())
}

func sharedReplicaPath(ws *workspace.Workspace, service string) *mcp.CallToolResult {
	rw, err := replica.Resolve(ws)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	if service == "" {
		return mcp.NewToolResultText(replica.Root(ws))
	}
	svc, ok := rw.Services[service]
	if !ok || !rw.IsShared(service) {
		return mcp.NewToolResultError(fmt.Sprintf("%q is not a shared service of workspace %q. The shared services are: %s", service, ws.Name, strings.Join(rw.SharedNames(), ", ")))
	}
	return mcp.NewToolResultText(svc.RepoPath)
}

func sharedReplicaBuild(ws *workspace.Workspace, sb *strings.Builder) error {
	res, err := replica.Ensure(ws)
	if err != nil {
		return err
	}
	for _, wt := range res.Created {
		fmt.Fprintf(sb, "  built replica worktree %-16s %s (%s)\n", wt.Repo, wt.Path, wt.Branch)
		if len(wt.Services) > 1 || (len(wt.Services) == 1 && wt.Services[0] != wt.Repo) {
			fmt.Fprintf(sb, "    it holds the services %s\n", strings.Join(wt.Services, ", "))
		}
	}
	for _, name := range res.Removed {
		fmt.Fprintf(sb, "  removed replica worktree %s — the manifest no longer lists it\n", name)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(sb, "warning: %s\n", w)
	}
	return nil
}

func sharedReplicaSync(ws *workspace.Workspace) *mcp.CallToolResult {
	var sb strings.Builder
	if !config.HasWorkspaceManifest(replica.Root(ws)) {
		sb.WriteString("There is no replica yet, so devstack builds it first.\n")
		if err := sharedReplicaBuild(ws, &sb); err != nil {
			return mcp.NewToolResultError(sb.String() + err.Error())
		}
	}
	if err := hostdaemon.SyncShared(&sb, ws, sharedRestarter(&sb, ws.Name)); err != nil {
		return mcp.NewToolResultError(sb.String() + err.Error())
	}
	return mcp.NewToolResultText(sb.String())
}
