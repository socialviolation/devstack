package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/workspace"
)

// registerMigrateTool exposes the migrations to an agent. The report of a run
// ends with a NEXT block that is addressed to an agent, and until this tool
// existed the only way to produce that block was a shell.
//
// It renders through migrate.Sweep, which is the same call the CLI makes, so
// the two surfaces can not report one machine differently.
func registerMigrateTool(mcpServer *server.MCPServer, ws *workspace.Workspace, patches []migrate.Patch) {
	tool := mcp.NewTool("migrate",
		mcp.WithDescription("Run every migration that this devstack needs, or preview them. A migration moves the workspace configuration from one version to the next. The version is the number at the top of devstack.workspace.yaml, which is committed, so a clone of a migrated repository needs no migration. devstack runs each pending migration over every workspace on this machine, and not over this workspace alone.\n"+
			"action=\"list\" reads only. It prints the version of each workspace, and the version this devstack needs. It changes no file.\n"+
			"action=\"run\" applies each pending migration, and then writes the new version into each manifest. It changes files.\n"+
			"CAUTION: devstack does not own the service repositories. A run writes .mcp.json and .claude/settings.json in each repository of each workspace. It removes the devstack block from AGENTS.md, CLAUDE.md, GEMINI.md, .cursorrules and .github/copilot-instructions.md. It deletes a file that holds that block and nothing else. devstack removes only what devstack wrote. Your own text stays, byte for byte.\n"+
			"Before a run writes, devstack checks each file that it strips or deletes. If one of them holds a change that nobody committed, devstack refuses, and it changes no file in any workspace. git holds no copy of that change, so the change can not come back. Commit or stash the file, or set force=true.\n"+
			"A run makes a real git diff in each repository it writes in. devstack does not commit, and it does not push. Read the diff yourself, and commit it in that repository. The version reaches your teammates only after you commit devstack.workspace.yaml.\n"+
			"A run that changed something ends with a NEXT block. That block is the work this tool can not do for you:\n"+
			"- Commit each diff.\n"+
			"- Restart the session, so that the tools of a new .mcp.json load.\n"+
			"If that block is there, read it and do what it says. A run that changed nothing, and every action=\"list\", ends with its own closing line instead.\n"+
			"This tool builds no replica and starts nothing. To find a workspace with no replica, run 'devstack workspace doctor'.\n"+
			"A second run changes nothing. This tool mirrors 'devstack migrate' and 'devstack migrate --list'.\n"+
			"action=\"shared-stacks\" converts THIS workspace to the shared set and small stacks. Without apply=true, it changes nothing and prints what it changes. It mirrors 'devstack migrate shared-stacks'. Before it writes, devstack copies each file it edits to a backup directory, and prints that path."),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("\"list\" prints the version of each workspace, and it changes nothing. Use it first: it reports what a run changes. \"run\" applies each pending migration. It writes and deletes files in repositories that devstack does not own. \"shared-stacks\" converts this workspace to the shared set. It changes nothing without apply=true.")),
		mcp.WithBoolean("force",
			mcp.Description("Migrate a file that holds a change nobody committed. Without it, devstack refuses such a file and changes nothing in any workspace. With it, devstack removes its block from that file, or deletes the file, and the change is lost.")),
		mcp.WithBoolean("apply",
			mcp.Description("Only for action=\"shared-stacks\". Write the changes. The default is false, and then devstack changes nothing.")),
		mcp.WithString("shared",
			mcp.Description("Only for action=\"shared-stacks\". The shared services, separated by commas. With apply=true, devstack writes this list to shared:.")),
		mcp.WithString("needs",
			mcp.Description("Only for action=\"shared-stacks\". Make an edge a needs edge, as svc=dep. Separate more than one with commas.")),
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
		case "shared-stacks":
			return migrateSharedStacks(ws, request), nil
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use \"list\", \"run\" or \"shared-stacks\"", action)), nil
		}

		all, err := migrate.Workspaces()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var sb strings.Builder
		if err := migrate.Sweep(&sb, patches, all, run, request.GetBool("force", false)); err != nil {
			return mcp.NewToolResultError(strings.TrimRight(sb.String(), "\n") + "\n" + err.Error()), nil
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func migrateSharedStacks(ws *workspace.Workspace, request mcp.CallToolRequest) *mcp.CallToolResult {
	if ws == nil {
		return mcp.NewToolResultError("devstack resolved no workspace, so it can not migrate one")
	}
	opts := migrate.SharedStacksOptions{Apply: request.GetBool("apply", false)}
	if raw, ok := request.GetArguments()["shared"]; ok && raw != nil {
		opts.Shared = migrate.SplitNames(fmt.Sprintf("%v", raw))
	}
	needs, err := migrate.ParseNeeds(migrate.SplitNames(request.GetString("needs", "")))
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	opts.Needs = needs
	var sb strings.Builder
	if err := migrate.SharedStacks(&sb, ws, opts); err != nil {
		return mcp.NewToolResultError(strings.TrimRight(sb.String(), "\n") + "\n" + err.Error())
	}
	return mcp.NewToolResultText(sb.String())
}
