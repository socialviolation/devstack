package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/migrate"
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

	pending, why, err := detectAgentFiles(ws)
	if err != nil {
		t.Fatalf("detectAgentFiles() = %v", err)
	}
	if !pending {
		t.Fatal("a workspace that holds devstack blocks must read as pending")
	}
	if !strings.Contains(why, "4 files hold a devstack block") {
		t.Errorf("the detector says %q, which does not count the files", why)
	}

	res, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("runAgentFiles() = %v", err)
	}
	if !res.Changed {
		t.Fatalf("the first run reports no change:\n%s", strings.Join(res.Lines, "\n"))
	}
	if len(res.Items) != 3 {
		t.Errorf("the run names %d changed repositories, want 3: %+v", len(res.Items), res.Items)
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

	pending, why, err = detectAgentFiles(ws)
	if err != nil {
		t.Fatalf("detectAgentFiles() = %v", err)
	}
	if pending {
		t.Fatalf("the sweep is not idempotent: it is still pending with %q", why)
	}

	second, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("second runAgentFiles() = %v", err)
	}
	if second.Changed || len(second.Lines) != 0 {
		t.Fatalf("the second run changed something:\n%s", strings.Join(second.Lines, "\n"))
	}
}

// The record is a log and never a gate for this patch: the sweep is cheap and
// idempotent, so a repository cloned after the record was written still gets
// cleaned. A record that could hide a stale AGENTS.md would be worse than none.
func TestTheAgentFilesPatchAnswersToTheFilesystemAndNotTheRecord(t *testing.T) {
	if !agentFilesPatch().Rescan {
		t.Fatal("the file sweep must rescan, so a record can never hide a repository that still holds a block")
	}
	if replicaPatch().Rescan {
		t.Error("the replica build must not rescan: a user who removed a replica did that on purpose")
	}
}

// The note is the last thing an agent reads before it decides what to do, so it
// has to name every repository that changed and give the command that finishes
// the job.
func TestTheAgentFilesNoteNamesEveryChangedRepositoryAndTheCommitCommand(t *testing.T) {
	home := t.TempDir()
	api := filepath.Join(home, "dev", "navexa", "api")
	stacked := filepath.Join(home, "dev", ".devstack-stacks", "navexa", "feat", "web")
	gitRepoWith(t, api, map[string]string{".": "api"})
	gitRepoWith(t, stacked, map[string]string{".": "web"})

	got := strings.Join(nextAgentFiles([]migrate.Result{{
		Workspace: "navexa",
		Items: []migrate.Item{
			{Label: "api", Path: api},
			{Label: "web (stack feat)", Path: stacked},
		},
	}}), "\n")

	for _, want := range []string{
		"NOW COMMIT",
		"navexa",
		api,
		stacked,
		commitCommand,
		"next clone",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the note never states %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "push it") || strings.Contains(got, "git push") {
		t.Errorf("devstack must not tell anybody to push:\n%s", got)
	}
}

// The replica note answers a different question from the file sweep. A commit is
// the wrong next action here: the services that run are the thing that is now
// out of date.
func TestTheReplicaNoteAsksForARestartAndNeverForACommit(t *testing.T) {
	got := strings.Join(nextReplica([]migrate.Result{{
		Workspace: "navexa",
		Items:     []migrate.Item{{Label: "navexa", Path: "/home/nick/dev/.devstack-base/navexa"}},
	}}), "\n")

	for _, want := range []string{
		"RESTART YOUR SERVICES",
		"/home/nick/dev/.devstack-base/navexa",
		"devstack service restart <svc> --stack base",
		"devstack base sync",
		"default branch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the note never states %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, commitCommand) {
		t.Errorf("a replica build produces no diff to commit:\n%s", got)
	}
}

// A file devstack can not migrate is named, and it never becomes an instruction
// to commit work that does not exist.
func TestTheReportAsksForAHumanAndCountsNoChange(t *testing.T) {
	got := strings.Join(agentFilesCounts(migrateResult{NeedsHuman: 1}), "\n")

	if !strings.Contains(got, "needs a human") {
		t.Errorf("the report never asks for a human:\n%s", got)
	}
	if strings.Contains(got, commitCommand) {
		t.Errorf("devstack changed nothing, so it must not ask for a commit:\n%s", got)
	}
}

// --list is the read-only view: it shows the patch that is done and the patch
// that is not, and it changes nothing.
func TestListShowsEveryPatchAppliedAndPending(t *testing.T) {
	ws, svcDir, _ := setupWorkspaceWithStack(t)
	writeFile(t, filepath.Join(svcDir, agentsFileName), "# api\n\nMine.\n\n"+block("generated")+"\n")
	if err := migrate.Append(migrate.Record{ID: "0.2.0-replica", Workspace: "navexa", AppliedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	st, err := migrate.List(patches(), []workspace.Workspace{*ws})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	migrate.WriteList(&b, st)
	got := b.String()

	for _, want := range []string{
		"0.2.0-agent-files", "pending", "a devstack block",
		"0.2.0-replica", "applied on",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("--list never states %q:\n%s", want, got)
		}
	}
	if len(scanResidue(svcDir)) != 1 {
		t.Error("--list changed a file")
	}
}
