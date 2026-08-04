package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/workspace"
)

// dirtyBlockEdit puts an edit inside the devstack block of one file, and leaves
// it uncommitted. The committed copy never holds that edit, so a strip that runs
// destroys it and git can not give it back.
func dirtyBlockEdit(t *testing.T, dir, rel string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, rel),
		"# api\n\nMine, and it stays.\n\n"+block("Old devstack instructions.\n\nMY UNCOMMITTED EDIT.")+"\n")
}

// A migration that removes a block from a file, or deletes it, destroys any
// change that nobody committed. The change is not in git, so no command brings
// it back. devstack has to stop before it writes.
func TestMigrateRefusesAFileThatHoldsAnUncommittedChange(t *testing.T) {
	ws, svcDir := migrateToolWorkspace(t)
	dirtyBlockEdit(t, svcDir, agentsFileName)

	blocks := preflightAgentFiles([]workspace.Workspace{*ws})
	if len(blocks) != 1 {
		t.Fatalf("the check found %d files, want 1: %+v", len(blocks), blocks)
	}
	if blocks[0].File != agentsFileName || blocks[0].Dir != svcDir || blocks[0].Label != "api" {
		t.Errorf("the check names %+v, which does not name the file and its repository", blocks[0])
	}

	var b strings.Builder
	if err := migrate.Apply(&b, patches(), []workspace.Workspace{*ws}, false); err == nil {
		t.Fatal("Apply() returned no error, so a caller can not tell that it refused")
	}

	got := b.String()
	for _, want := range []string{
		"REFUSES to migrate",
		"It changed no file",
		"api",
		agentsFileName,
		"nobody committed",
		"can not come back",
		"commit it or stash it",
		"devstack migrate --force",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal never states %q:\n%s", want, got)
		}
	}

	if !strings.Contains(readString(t, filepath.Join(svcDir, agentsFileName)), "MY UNCOMMITTED EDIT") {
		t.Error("the refusal destroyed the change it refused to destroy")
	}
	if _, err := os.Stat(filepath.Join(svcDir, ".mcp.json")); !os.IsNotExist(err) {
		t.Errorf("the refusal still wrote .mcp.json (stat err = %v)", err)
	}
}

// A refusal is not a failed write. It has to stop the whole patch, in every
// workspace, before anything changes: a machine half migrated is worse than one
// that is not migrated at all.
func TestARefusalInOneRepositoryStopsTheWholeMigration(t *testing.T) {
	ws, svcDir := migrateToolWorkspace(t)
	dirtyBlockEdit(t, svcDir, agentsFileName)

	var b strings.Builder
	if err := migrate.Apply(&b, patches(), []workspace.Workspace{*ws}, false); err == nil {
		t.Fatal("Apply() returned no error")
	}

	if _, err := os.Stat(filepath.Join(svcDir, "CLAUDE.md")); err != nil {
		t.Errorf("the refusal deleted CLAUDE.md in the same repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, claudeSettingsRel)); !os.IsNotExist(err) {
		t.Errorf("the refusal still wired the workspace root (stat err = %v)", err)
	}
	manifest := readString(t, filepath.Join(ws.Path, "devstack.workspace.yaml"))
	if !strings.HasPrefix(manifest, "version: 1") {
		t.Errorf("the refusal moved the workspace version:\n%s", manifest)
	}
}

// --force is the way forward for somebody who does not want the change. It says
// what it costs before it costs it.
func TestForceMigratesOverTheRefusalAndSaysWhatItLoses(t *testing.T) {
	ws, svcDir := migrateToolWorkspace(t)
	dirtyBlockEdit(t, svcDir, agentsFileName)

	var b strings.Builder
	if err := migrate.Apply(&b, patches(), []workspace.Workspace{*ws}, true); err != nil {
		t.Fatalf("Apply(force) = %v", err)
	}

	got := b.String()
	for _, want := range []string{"You gave --force", "each change is lost", agentsFileName} {
		if !strings.Contains(got, want) {
			t.Errorf("--force never states %q:\n%s", want, got)
		}
	}
	if strings.Contains(readString(t, filepath.Join(svcDir, agentsFileName)), "MY UNCOMMITTED EDIT") {
		t.Error("--force left the block in place, so it did not migrate")
	}
	if _, err := os.Stat(filepath.Join(svcDir, ".mcp.json")); err != nil {
		t.Errorf("--force wrote no .mcp.json: %v", err)
	}
}

// devstack generates .mcp.json whole, and it merges its hook into a settings
// file that keeps every other key. Neither one removes anything, so a diff there
// is a diff a reader can read and revert. A refusal about them would stop every
// machine that has ever run this migration, and it would protect nothing.
func TestADirtyGeneratedFileIsNotARefusal(t *testing.T) {
	ws, svcDir := migrateToolWorkspace(t)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(svcDir, claudeSettingsRel)), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(svcDir, ".mcp.json"), "{\"mcpServers\":{\"mine\":{}}}\n")
	writeFile(t, filepath.Join(svcDir, claudeSettingsRel), "{\"mine\":\"edited\"}\n")
	toolGit(t, svcDir, "add", "-f", ".")
	toolGit(t, svcDir, "commit", "-q", "-m", "generated files")
	writeFile(t, filepath.Join(svcDir, ".mcp.json"), "{\"mcpServers\":{\"mine\":{\"edited\":true}}}\n")
	writeFile(t, filepath.Join(svcDir, claudeSettingsRel), "{\"mine\":\"edited again\"}\n")

	if blocks := preflightAgentFiles([]workspace.Workspace{*ws}); len(blocks) != 0 {
		t.Fatalf("the check refuses a generated file: %+v", blocks)
	}
}

// devstack changes nothing in a file whose marker has no pair. Nothing there is
// lost, so a refusal about it is a refusal nobody can act on.
func TestAFileDevstackLeavesAloneIsNotARefusal(t *testing.T) {
	ws, svcDir := migrateToolWorkspace(t)
	writeFile(t, filepath.Join(svcDir, agentsFileName), "# api\n\n"+agentsSentinelBegin+"\ntruncated\n")

	if blocks := preflightAgentFiles([]workspace.Workspace{*ws}); len(blocks) != 0 {
		t.Fatalf("the check refuses a file devstack does not change: %+v", blocks)
	}
}

// A file with no change of its own is the normal state, and the migration has to
// run there. A guard that stops a clean machine is a guard nobody keeps.
func TestACleanRepositoryIsNotARefusal(t *testing.T) {
	ws, _ := migrateToolWorkspace(t)

	if blocks := preflightAgentFiles([]workspace.Workspace{*ws}); len(blocks) != 0 {
		t.Fatalf("the check refuses a clean repository: %+v", blocks)
	}
}

// The MCP tool and the CLI run the same check, so an agent can not migrate over
// a change that the command refuses. force is the same way forward, and the tool
// says what it costs.
func TestTheMigrateToolRefusesAndForceProceeds(t *testing.T) {
	ws, svcDir := migrateToolWorkspace(t)
	dirtyBlockEdit(t, svcDir, agentsFileName)
	s := migrateToolServer(t, ws)

	refused := migrateToolCall(t, s, map[string]any{"action": "run"})
	for _, want := range []string{"REFUSES to migrate", agentsFileName, "force"} {
		if !strings.Contains(refused, want) {
			t.Errorf("the tool never states %q:\n%s", want, refused)
		}
	}
	if !strings.Contains(readString(t, filepath.Join(svcDir, agentsFileName)), "MY UNCOMMITTED EDIT") {
		t.Fatal("the tool destroyed the change it refused to destroy")
	}

	forced := migrateToolCall(t, s, map[string]any{"action": "run", "force": true})
	if !strings.Contains(forced, "You gave --force") {
		t.Errorf("force never says what it overrides:\n%s", forced)
	}
	if strings.Contains(readString(t, filepath.Join(svcDir, agentsFileName)), "MY UNCOMMITTED EDIT") {
		t.Error("force left the block in place, so it did not migrate")
	}
}
