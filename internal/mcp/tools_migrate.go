package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/migrate"
)

// registerMigrateTool exposes the migrations to an agent. The report of a run
// ends with a NEXT block that is addressed to an agent, and until this tool
// existed the only way to produce that block was a shell.
//
// It renders through migrate.Sweep, which is the same call the CLI makes, so
// the two surfaces can not report one machine differently.
func registerMigrateTool(mcpServer *server.MCPServer, patches []migrate.Patch) {
	tool := mcp.NewTool("migrate",
		mcp.WithDescription("Run every migration that this devstack needs, or preview them. A migration moves the workspace configuration from one version to the next. The version is the number at the top of devstack.workspace.yaml, which is committed, so a clone of a migrated repository needs no migration. devstack runs each pending migration over every workspace on this machine, and not over this workspace alone.\n"+
			"action=\"list\" reads only. It prints the version of each workspace, and the version this devstack needs. It changes no file.\n"+
			"action=\"run\" applies each pending migration, and then writes the new version into each manifest. It changes files.\n"+
			"CAUTION: devstack does not own the service repositories. A run writes .mcp.json and .claude/settings.json in each repository of each workspace. It removes the devstack block from AGENTS.md, CLAUDE.md, GEMINI.md, .cursorrules and .github/copilot-instructions.md. It deletes a file that holds that block and nothing else. devstack removes only what devstack wrote. Your own text stays, byte for byte.\n"+
			"A run makes a real git diff in each repository it writes in. devstack does not commit, and it does not push. Read the diff yourself, and commit it in that repository. The version reaches your teammates only after you commit devstack.workspace.yaml.\n"+
			"The report ends with a NEXT block. That block is the work this tool can not do for you:\n"+
			"- Commit each diff.\n"+
			"- Restart the session, so that the tools of a new .mcp.json load.\n"+
			"Read that block, and do what it says.\n"+
			"This tool builds no replica and starts nothing. To find a workspace with no replica, run 'devstack workspace doctor'.\n"+
			"A second run changes nothing. This tool mirrors 'devstack migrate' and 'devstack migrate --list'."),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("\"list\" prints the version of each workspace, and it changes nothing. Use it first: it reports what a run changes. \"run\" applies each pending migration. It writes and deletes files in repositories that devstack does not own.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		action := request.GetString("action", "")
		var run bool
		switch action {
		case "list":
		case "run":
			run = true
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use \"list\" or \"run\"", action)), nil
		}

		all, err := migrate.Workspaces()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var sb strings.Builder
		if err := migrate.Sweep(&sb, patches, all, run); err != nil {
			return mcp.NewToolResultError(strings.TrimRight(sb.String(), "\n") + "\n" + err.Error()), nil
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}
