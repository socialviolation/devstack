package mcp

import (
	"context"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/doctor"
	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerDoctorTools(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	registerDoctorTool(mcpServer, ws, doctor.Live)
	registerCleanupTool(mcpServer, ws, doctor.Live)
}

func registerDoctorTool(mcpServer *server.MCPServer, ws *workspace.Workspace, host func(*workspace.Workspace) doctor.Host) {
	tool := mcp.NewTool("doctor",
		mcp.WithDescription("Find what is broken in this workspace, and name the fix for each problem. Call it when a service will not start, a stack acts strangely, or before you report that the environment is broken. "+
			"It checks the host daemon (down, or still bound to 0.0.0.0), each copy in error or missing from an up stack, ports a foreign process holds, stacks whose worktree is gone, stack branches gone from origin or behind it, expired stacks, copies with no tailnet address, the replica, and orphans: port allocations, directories and git worktrees that belong to no stack. "+
			"The answer groups problems as broken, degraded or tidy-up. Each problem is one line, with the fix under it. A healthy workspace gives one line. It reads state only, and changes nothing. It prints no env value. "+
			"This tool mirrors 'devstack doctor'."),
		mcp.WithString("stack",
			mcp.Description("Check one feature stack, by short name. With no stack, the tool checks base, every stack and the orphans of this workspace.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace to check"), nil
		}
		report, err := doctor.Check(ws, strings.TrimSpace(request.GetString("stack", "")), host(ws))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(report.Render(auth.CallerFrom(ctx).Scope != auth.ScopeOwner)), nil
	})
}

func registerCleanupTool(mcpServer *server.MCPServer, ws *workspace.Workspace, host func(*workspace.Workspace) doctor.Host) {
	tool := mcp.NewTool("cleanup",
		mcp.WithDescription("Remove the orphans of this workspace: port allocations with no stack, directories and git worktrees under the stacks root with no stack, git worktree entries whose directory is gone, and the records of stacks whose directory is gone. "+
			"It never touches base, a stack whose directory exists, or a stack that is up. It keeps a worktree that holds uncommitted work, and says so. It ignores anything younger than 10 minutes, because a stack that is being created looks like an orphan. "+
			"dry_run defaults to true: the tool then lists exactly what it would do, and changes nothing. To find the orphans first, call doctor. "+
			"This tool mirrors 'devstack cleanup'."),
		mcp.WithBoolean("dry_run",
			mcp.Description("If true, list what the tool would remove, and remove nothing. The default is true.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace to clean up"), nil
		}
		apply := !request.GetBool("dry_run", true)
		actions, err := doctor.Cleanup(ws, apply, host(ws))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(doctor.RenderCleanup(actions, apply, "To do it, call cleanup with dry_run=false.")), nil
	})
}
