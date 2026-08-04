package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// setupWorkspaceWithStack builds a registered workspace of one service, plus one
// feature stack whose worktree holds the same service, under an isolated HOME.
func setupWorkspaceWithStack(t *testing.T) (ws *workspace.Workspace, svcDir, stackSvcDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	stackRoot := filepath.Join(home, "dev", ".devstack-stacks", "navexa", "feat")
	svcDir = filepath.Join(root, "api")
	stackSvcDir = filepath.Join(stackRoot, "api")

	for _, d := range []string{svcDir, stackSvcDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	manifest := "version: 1\nworkspace:\n  name: navexa\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./api\n"
	service := "version: 1\nservice:\n  name: api\nruntime:\n  run:\n    command: go run .\n"
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), manifest)
	writeFile(t, filepath.Join(stackRoot, "devstack.workspace.yaml"), manifest)
	writeFile(t, filepath.Join(svcDir, "devstack.service.yaml"), service)
	writeFile(t, filepath.Join(stackSvcDir, "devstack.service.yaml"), service)

	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: root, TiltPort: 10350}); err != nil {
		t.Fatalf("register: %v", err)
	}
	recs := []stack.Record{{
		Name: "feat", Base: "navexa", Root: stackRoot, Branch: "nick/feat",
		Overlay: []string{"api"}, Worktrees: map[string]string{"api": stackSvcDir},
	}}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := workspace.DataDir("navexa")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "stacks.json"), string(data))

	return &workspace.Workspace{Name: "navexa", Path: root, TiltPort: 10350}, svcDir, stackSvcDir
}

// A stack worktree lives outside the service paths of the workspace, so nothing
// else reaches it. A migration that skipped it would leave the stale
// instructions exactly where an agent is most likely to read them.
func TestMigrateSweepsTheRootEveryServiceAndEveryStackWorktree(t *testing.T) {
	ws, svcDir, stackSvcDir := setupWorkspaceWithStack(t)

	targets, errs := migrateTargets(ws)
	if len(errs) > 0 {
		t.Fatalf("migrateTargets() errors = %v", errs)
	}

	dirs := map[string]string{}
	for _, tgt := range targets {
		dirs[tgt.Dir] = tgt.Label
	}
	for _, want := range []string{ws.Path, svcDir, stackSvcDir} {
		if _, ok := dirs[want]; !ok {
			t.Errorf("migrateTargets() never reaches %s: %+v", want, targets)
		}
	}
	if label := dirs[stackSvcDir]; !strings.Contains(label, "stack feat") {
		t.Errorf("the stack worktree is labelled %q, which does not name its stack", label)
	}
	if dirs[ws.Path] != "workspace root" {
		t.Errorf("the workspace root is labelled %q", dirs[ws.Path])
	}
}

// The whole sweep, end to end: the block goes, the file that held nothing else
// goes, the configuration arrives, and the second run has nothing left to do.
func TestMigrateRemovesTheBlockEverywhereAndIsIdempotent(t *testing.T) {
	ws, svcDir, stackSvcDir := setupWorkspaceWithStack(t)

	for _, dir := range []string{svcDir, stackSvcDir} {
		writeFile(t, filepath.Join(dir, agentsFileName),
			"# api\n\nMine, and it stays.\n\n"+block("generated instructions")+"\n")
		writeFile(t, filepath.Join(dir, "CLAUDE.md"), block("a pointer block")+"\n")
	}

	targets, _ := migrateTargets(ws)
	var first migrateResult
	var b strings.Builder
	first.add(migrateWorkspace(&b, ws.Name, targets))

	if first.Removed != 2 || first.Deleted != 2 {
		t.Errorf("first run removed %d blocks and deleted %d files, want 2 and 2:\n%s", first.Removed, first.Deleted, b.String())
	}
	if first.MCP != 2 || first.Hooks != 3 {
		t.Errorf("first run wrote %d .mcp.json and %d hooks, want 2 and 3:\n%s", first.MCP, first.Hooks, b.String())
	}
	for _, dir := range []string{svcDir, stackSvcDir} {
		got := readString(t, filepath.Join(dir, agentsFileName))
		if strings.Contains(got, agentsSentinelBegin) {
			t.Errorf("%s still holds a devstack block:\n%s", dir, got)
		}
		if !strings.Contains(got, "Mine, and it stays.") {
			t.Errorf("%s lost the text a human wrote:\n%s", dir, got)
		}
		if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
			t.Errorf("%s/CLAUDE.md held devstack content only, so it must be gone (stat err = %v)", dir, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err != nil {
			t.Errorf("%s has no .mcp.json: %v", dir, err)
		}
	}

	var second migrateResult
	var b2 strings.Builder
	second.add(migrateWorkspace(&b2, ws.Name, targets))
	if second.Changed() != 0 || second.NeedsHuman != 0 {
		t.Fatalf("the second run changed %d files:\n%s", second.Changed(), b2.String())
	}
	if b2.String() != "" {
		t.Fatalf("the second run printed a report with nothing in it:\n%s", b2.String())
	}
}

// The note is the last thing an agent reads before it decides what to do, so it
// has to say what changed, name every repository that changed, and give the
// command that finishes the job.
func TestTheSummaryNamesEveryChangedRepositoryAndTheCommitCommand(t *testing.T) {
	res := migrateResult{
		Removed: 2, Deleted: 1, MCP: 2, Hooks: 2,
		Repos: []migrateTarget{
			{Label: "api", Dir: "/home/nick/dev/navexa/api"},
			{Label: "web (stack feat)", Dir: "/home/nick/dev/.devstack-stacks/navexa/feat/web"},
		},
	}

	var b strings.Builder
	writeMigrateSummary(&b, res)
	got := b.String()

	for _, want := range []string{
		"7 files", "2 repositories",
		"/home/nick/dev/navexa/api",
		"/home/nick/dev/.devstack-stacks/navexa/feat/web",
		commitCommand,
		"next clone",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the summary never states %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "push it") || strings.Contains(got, "git push") {
		t.Errorf("devstack must not tell anybody to push:\n%s", got)
	}
}

// A run that changed nothing must not tell a reader to commit an empty diff.
func TestTheSummaryIsSilentWhenNothingChanged(t *testing.T) {
	var b strings.Builder
	writeMigrateSummary(&b, migrateResult{})
	got := b.String()

	if !strings.Contains(got, "changed no file") {
		t.Errorf("a run that changed nothing must say so:\n%s", got)
	}
	if strings.Contains(got, commitCommand) || strings.Contains(got, "COMMIT") {
		t.Errorf("nothing changed, so the summary must not ask for a commit:\n%s", got)
	}
}

// A file devstack can not migrate is named, and it never becomes an instruction
// to commit work that does not exist.
func TestTheSummaryAsksForAHumanAndCountsNoChange(t *testing.T) {
	var b strings.Builder
	writeMigrateSummary(&b, migrateResult{NeedsHuman: 1})
	got := b.String()

	if !strings.Contains(got, "needs a human") {
		t.Errorf("the summary never asks for a human:\n%s", got)
	}
	if strings.Contains(got, commitCommand) {
		t.Errorf("devstack changed nothing, so it must not ask for a commit:\n%s", got)
	}
}

// A file that needs a human is not a reason to commit: devstack changed nothing,
// so the note must say so and stop.
func TestTheSummarySaysNothingChangedWhenOnlyAHumanIsNeeded(t *testing.T) {
	var b strings.Builder
	writeMigrateSummary(&b, migrateResult{NeedsHuman: 2})

	if !strings.Contains(b.String(), "nothing to commit") {
		t.Errorf("the summary never says that nothing changed:\n%s", b.String())
	}
}
