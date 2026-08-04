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
		mcp.WithDescription("Run every migration that this devstack needs, or preview them. A migration is a patch: one versioned unit of work with its own detector. devstack runs each pending patch over every workspace on this machine, and not over this workspace alone.\n"+
			"action=\"list\" reads only. It prints each patch, applied or pending, with the reason. It changes no file.\n"+
			"action=\"run\" applies each pending patch. It changes files.\n"+
			"CAUTION: devstack does not own the service repositories. A run writes .mcp.json and .claude/settings.json in each repository of each workspace. It removes the devstack block from AGENTS.md, CLAUDE.md, GEMINI.md, .cursorrules and .github/copilot-instructions.md. It deletes a file that holds nothing but that block. It also cuts one git worktree for each repository, to build the replica that base runs from. devstack removes only what devstack wrote, and the text of a human stays byte for byte.\n"+
			"A run makes a real git diff in each repository it writes in. devstack does not commit, and it does not push. Read the diff yourself, and commit it in that repository.\n"+
			"The report ends with a NEXT block. That block is the work this tool can not do for you: commit each diff, restart the session so that the tools of a new .mcp.json load, and restart the services that run from a new replica. Read it, and do what it says.\n"+
			"A second run changes nothing. This tool mirrors 'devstack migrate' and 'devstack migrate --list'."),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("\"list\" prints each patch, applied or pending, and it changes nothing. Use it first to see what a run would do. \"run\" applies each pending patch. It writes and deletes files in repositories that devstack does not own, and it cuts git worktrees.")),
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

		var sb strings.Builder
		err := migrate.Sweep(&sb, patches, migrate.Workspaces(), run)
		if err != nil {
			return mcp.NewToolResultError(strings.TrimRight(sb.String(), "\n") + "\n" + err.Error()), nil
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}
