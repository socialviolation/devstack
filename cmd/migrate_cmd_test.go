package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
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

	second, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("second runAgentFiles() = %v", err)
	}
	if second.Changed || len(second.Lines) != 0 {
		t.Fatalf("the second run changed something:\n%s", strings.Join(second.Lines, "\n"))
	}
}

// A migration is one-off. Adding a service to a workspace must not make a
// migration pending again: the version in the manifest says the work is done,
// and the doctor reports the service that nobody connected.
func TestTheAgentFilesMigrationStaysAppliedWhenAServiceIsAdded(t *testing.T) {
	ws, svcDir, _ := setupWorkspaceWithStack(t)
	writeFile(t, filepath.Join(svcDir, agentsFileName), "# api\n\nMine.\n\n"+block("generated")+"\n")

	only := []migrate.Patch{agentFilesPatch()}
	if err := migrate.Apply(&strings.Builder{}, only, []workspace.Workspace{*ws}, false); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	// A second service arrives: unconnected, and with nothing committed.
	web := filepath.Join(ws.Path, "web")
	writeAt(t, filepath.Join(web, "devstack.service.yaml"), serviceManifest("web"))
	if err := config.AddServiceRepo(ws.Path, "./web"); err != nil {
		t.Fatal(err)
	}

	st := migrate.List(only, []workspace.Workspace{*ws})
	if st[0].Pending() {
		t.Errorf("migration %s is pending again after a service was added: %+v", st[0].Name(), st[0].Rows)
	}

	if !wiringPending(migrateTarget{Label: "web", Dir: web, Service: "web"}) {
		t.Error("the new service is not connected to devstack, and the doctor's check must say so")
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

// A file devstack could not finish keeps the migration pending. If the version
// moved, the block would stay forever: the doctor would keep reporting the file,
// and its remedy, `devstack migrate`, would keep answering that there is nothing
// to do.
func TestAFileThatNeedsAHumanLeavesTheVersionAlone(t *testing.T) {
	ws, svcDir, _ := setupWorkspaceWithStack(t)
	writeFile(t, filepath.Join(svcDir, "CLAUDE.md"), "# api\n\n"+agentsSentinelBegin+"\n\ntruncated\n")

	only := []migrate.Patch{agentFilesPatch()}
	if err := migrate.Apply(&strings.Builder{}, only, []workspace.Workspace{*ws}, false); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	if v, err := config.WorkspaceVersion(ws.Path); err != nil || v != 1 {
		t.Errorf("the workspace is at version %d, want 1 while a file still needs a human (err = %v)", v, err)
	}
	if st := migrate.List(only, []workspace.Workspace{*ws}); !st[0].Pending() {
		t.Errorf("the migration is not pending, so the doctor's remedy has no exit: %+v", st[0].Rows)
	}

	res, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("second runAgentFiles() = %v", err)
	}
	if !res.Incomplete {
		t.Errorf("the run is not incomplete, and the file is still there:\n%s", strings.Join(res.Lines, "\n"))
	}
	if !strings.Contains(strings.Join(res.Lines, "\n"), "needs a human") {
		t.Errorf("the report no longer asks for a human:\n%s", strings.Join(res.Lines, "\n"))
	}

	if err := os.Remove(filepath.Join(svcDir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Apply(&strings.Builder{}, only, []workspace.Workspace{*ws}, false); err != nil {
		t.Fatalf("Apply() after the human acted = %v", err)
	}
	if v, err := config.WorkspaceVersion(ws.Path); err != nil || v != 2 {
		t.Errorf("the workspace is at version %d after the file went, want 2 (err = %v)", v, err)
	}
}

// --list is the read-only view. It reports by version, and it changes nothing.
func TestListReportsTheVersionAndChangesNothing(t *testing.T) {
	ws, svcDir, _ := setupWorkspaceWithStack(t)
	writeFile(t, filepath.Join(svcDir, agentsFileName), "# api\n\nMine.\n\n"+block("generated")+"\n")

	var b strings.Builder
	migrate.WriteList(&b, migrate.List(patches(), []workspace.Workspace{*ws}))
	got := b.String()

	for _, want := range []string{
		"version 1 to 2",
		"pending: this workspace is at version 1, and this devstack needs version 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("--list never states %q:\n%s", want, got)
		}
	}
	if len(scanResidue(svcDir)) != 1 {
		t.Error("--list changed a file")
	}
	if v, err := config.WorkspaceVersion(ws.Path); err != nil || v != 1 {
		t.Errorf("--list moved the workspace to version %d (err = %v)", v, err)
	}
}

// setupNestedServiceWorkspace builds a workspace whose services sit in
// subdirectories of one git repository, and a feature stack whose worktree has
// the same shape. An older devstack wrote its block at the root of each
// repository, and no service path names that root.
func setupNestedServiceWorkspace(t *testing.T) (ws *workspace.Workspace, repoRoot, stackRepoRoot string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	repoRoot = filepath.Join(root, "orbit")
	stackRoot := filepath.Join(home, "dev", ".devstack-stacks", "navexa", "feat")
	stackRepoRoot = filepath.Join(stackRoot, "orbit")

	for _, d := range []string{filepath.Join(repoRoot, "api"), filepath.Join(repoRoot, "web"), stackRoot} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	manifest := "version: 1\nworkspace:\n  name: navexa\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./orbit/api\n      - ./orbit/web\n"
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), manifest)
	writeFile(t, filepath.Join(stackRoot, "devstack.workspace.yaml"), manifest)
	writeFile(t, filepath.Join(repoRoot, "api", "devstack.service.yaml"),
		"version: 1\nservice:\n  name: orbit-api\nruntime:\n  run:\n    command: go run .\n")
	writeFile(t, filepath.Join(repoRoot, "web", "devstack.service.yaml"),
		"version: 1\nservice:\n  name: orbit-web\nruntime:\n  run:\n    command: npm start\n")
	writeFile(t, filepath.Join(repoRoot, agentsFileName),
		"# orbit\n\nMine, and it stays.\n\n"+block("Old devstack instructions.")+"\n")

	toolGit(t, repoRoot, "init", "-b", "main", "-q")
	toolGit(t, repoRoot, "config", "commit.gpgsign", "false")
	toolGit(t, repoRoot, "add", "-f", ".")
	toolGit(t, repoRoot, "commit", "-q", "-m", "init")
	toolGit(t, repoRoot, "worktree", "add", "-q", "-b", "nick/feat", stackRepoRoot)

	ws = &workspace.Workspace{Name: "navexa", Path: root, TiltPort: 10350}
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	recs := []stack.Record{{
		Name: "feat", Base: "navexa", Root: stackRoot, Branch: "nick/feat",
		Overlay:   []string{"orbit-api"},
		Worktrees: map[string]string{"orbit-api": filepath.Join(stackRepoRoot, "api")},
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

	return ws, repoRoot, stackRepoRoot
}

// An older devstack wrote its block at the root of each git repository. devstack
// sweeps service directories. Where the services of a repository sit below its
// root, nothing reaches the root, and the block stays exactly where an agent
// reads it first.
func TestMigrateStripsTheRootOfARepositoryWhoseServicesSitBelowIt(t *testing.T) {
	ws, repoRoot, _ := setupNestedServiceWorkspace(t)
	agents := filepath.Join(repoRoot, agentsFileName)

	if len(scanResidue(repoRoot)) != 1 {
		t.Fatalf("the test never put a block at %s", agents)
	}

	if _, err := runAgentFiles(ws); err != nil {
		t.Fatalf("runAgentFiles() = %v", err)
	}

	got := readString(t, agents)
	if strings.Contains(got, agentsSentinelBegin) {
		t.Errorf("the repository root still holds the devstack block:\n%s", got)
	}
	if !strings.Contains(got, "Mine, and it stays.") {
		t.Errorf("the strip lost the text a human wrote:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".mcp.json")); !os.IsNotExist(err) {
		t.Errorf("the repository root runs no service, so it gets no .mcp.json (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, claudeSettingsRel)); !os.IsNotExist(err) {
		t.Errorf("the repository root runs no service, so it gets no hook (stat err = %v)", err)
	}
}

// `workspace doctor` reads the same directories, so a root the sweep missed was
// a root the doctor called clean.
func TestTheResidueScanReachesTheRepositoryRoot(t *testing.T) {
	ws, repoRoot, stackRepoRoot := setupNestedServiceWorkspace(t)

	var paths []string
	for _, f := range workspaceResidue(ws) {
		paths = append(paths, f.Path)
	}
	for _, want := range []string{
		filepath.Join(repoRoot, agentsFileName),
		filepath.Join(stackRepoRoot, agentsFileName),
	} {
		if !slices.Contains(paths, want) {
			t.Errorf("the residue scan never reports %s: %v", want, paths)
		}
	}
}

// A stack worktree is its own git working tree on a feature branch. A root the
// sweep misses there keeps the block, and merging that branch later brings the
// block back into a base that has already deleted it.
func TestMigrateStripsTheRepositoryRootOfAStackWorktree(t *testing.T) {
	ws, _, stackRepoRoot := setupNestedServiceWorkspace(t)
	agents := filepath.Join(stackRepoRoot, agentsFileName)

	if len(scanResidue(stackRepoRoot)) != 1 {
		t.Fatalf("the worktree never held a block at %s", agents)
	}

	res, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("runAgentFiles() = %v", err)
	}
	if strings.Contains(readString(t, agents), agentsSentinelBegin) {
		t.Errorf("the worktree root still holds the devstack block:\n%s", readString(t, agents))
	}

	report := strings.Join(res.Lines, "\n")
	for _, want := range []string{
		stackRepoRoot,
		"committed nothing",
		"nick/feat",
		"devstack stack rm",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report never states %q:\n%s", want, report)
		}
	}
}

// devstack must not stage or commit in somebody's feature branch. The change is
// theirs to read and to resolve.
func TestMigrateLeavesTheWorktreeChangeUncommittedAndUnstaged(t *testing.T) {
	ws, _, stackRepoRoot := setupNestedServiceWorkspace(t)

	if _, err := runAgentFiles(ws); err != nil {
		t.Fatalf("runAgentFiles() = %v", err)
	}

	out := gitOutput(t, stackRepoRoot, "status", "--porcelain", "--", agentsFileName)
	if !strings.HasPrefix(out, " M") {
		t.Errorf("git reports %q for %s, want an unstaged change (\" M\")", out, agentsFileName)
	}
	if n := gitOutput(t, stackRepoRoot, "rev-list", "--count", "nick/feat"); n != "1" {
		t.Errorf("the branch nick/feat is at %s commits, want 1: devstack committed in it", n)
	}
}

// The refusal has to cover the directories the sweep now reaches. A repository
// root devstack strips without asking is a root where an uncommitted change dies.
func TestMigrateRefusesAnUncommittedFileAtARepositoryRoot(t *testing.T) {
	ws, repoRoot, _ := setupNestedServiceWorkspace(t)
	writeFile(t, filepath.Join(repoRoot, agentsFileName),
		"# orbit\n\nMine, and it stays.\n\n"+block("Old devstack instructions.\n\nMY UNCOMMITTED EDIT.")+"\n")

	blocks := preflightAgentFiles([]workspace.Workspace{*ws})
	var found bool
	for _, b := range blocks {
		if b.Dir == repoRoot && b.File == agentsFileName {
			found = true
		}
	}
	if !found {
		t.Fatalf("the check never names %s at the repository root: %+v", agentsFileName, blocks)
	}

	var b strings.Builder
	if err := migrate.Apply(&b, patches(), []workspace.Workspace{*ws}, false); err == nil {
		t.Fatal("Apply() returned no error, so a caller can not tell that it refused")
	}
	if !strings.Contains(readString(t, filepath.Join(repoRoot, agentsFileName)), "MY UNCOMMITTED EDIT") {
		t.Error("the refusal destroyed the change it refused to destroy")
	}
}

// gitOutput is the trimmed stdout of one git command.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimRight(string(out), "\n")
}

// `workspace doctor` asks migrateTargets which repositories devstack connects,
// and it offers `devstack init --all --claude-hook` to connect them. A
// repository root runs no service, so devstack writes no .mcp.json and no hook
// there. Naming a root in that list gives the reader a warning that no command
// clears.
func TestTheRepositoryRootIsSweptAndIsNotARepositoryToConnect(t *testing.T) {
	ws, repoRoot, _ := setupNestedServiceWorkspace(t)

	targets, errs := migrateTargets(ws)
	if len(errs) > 0 {
		t.Fatalf("migrateTargets() errors = %v", errs)
	}
	for _, tgt := range targets {
		if tgt.Dir == repoRoot {
			t.Errorf("migrateTargets() names the repository root %s, so the doctor asks a human to connect it", repoRoot)
		}
	}

	swept, errs := stripTargets(ws)
	if len(errs) > 0 {
		t.Fatalf("stripTargets() errors = %v", errs)
	}
	var found bool
	for _, tgt := range swept {
		if tgt.Dir == repoRoot {
			found = true
			if !tgt.StripOnly {
				t.Errorf("the repository root %s is not strip-only, so devstack writes to it", repoRoot)
			}
			if tgt.Service != "" {
				t.Errorf("the repository root %s names the service %q, so it gets a .mcp.json", repoRoot, tgt.Service)
			}
		}
	}
	if !found {
		t.Errorf("stripTargets() never reaches the repository root %s", repoRoot)
	}
}

// atCurrentVersion moves the workspace to the version this devstack needs, the
// way a finished migration leaves it. Everything below is about a machine where
// the migration is applied and the block came back anyway.
func atCurrentVersion(t *testing.T, ws *workspace.Workspace) {
	t.Helper()
	if err := config.SetWorkspaceVersion(ws.Path, 2, "test"); err != nil {
		t.Fatalf("SetWorkspaceVersion: %v", err)
	}
	if st := migrate.List(patches(), []workspace.Workspace{*ws}); st[0].Pending() {
		t.Fatalf("the workspace is still pending, so this tests the patch and not the repair: %+v", st[0].Rows)
	}
}

// The defect this repair closes. devstack removed the block, somebody committed
// that on a feature branch, and the squash merge kept the copy that still held
// it. The workspace never left the current version, so nothing version-gated can
// ever clear the block again.
func TestTheRepairStripsResidueInAWorkspaceAtTheCurrentVersion(t *testing.T) {
	ws, repoRoot, _ := setupNestedServiceWorkspace(t)
	atCurrentVersion(t, ws)
	agents := filepath.Join(repoRoot, agentsFileName)

	var b strings.Builder
	if err := migrate.Sweep(&b, patches(), []workspace.Workspace{*ws}, true, false); err != nil {
		t.Fatalf("Sweep() = %v", err)
	}

	got := readString(t, agents)
	if strings.Contains(got, agentsSentinelBegin) {
		t.Errorf("the block survived a workspace at the current version:\n%s", got)
	}
	if !strings.Contains(got, "Mine, and it stays.") {
		t.Errorf("the repair lost the text a human wrote:\n%s", got)
	}
	if report := b.String(); strings.Contains(report, "Do nothing.") {
		t.Errorf("the report tells the reader to do nothing, and it just changed a file:\n%s", report)
	}
}

// The repair must reach a running stack, or the branch keeps the block and a
// merge returns it to base. devstack never commits there, so the report has to
// say whose branch now holds an uncommitted change.
func TestTheRepairStripsAStackWorktreeAndNamesItsBranch(t *testing.T) {
	ws, _, stackRepoRoot := setupNestedServiceWorkspace(t)
	atCurrentVersion(t, ws)

	var b strings.Builder
	if err := migrate.Sweep(&b, patches(), []workspace.Workspace{*ws}, true, false); err != nil {
		t.Fatalf("Sweep() = %v", err)
	}

	if got := readString(t, filepath.Join(stackRepoRoot, agentsFileName)); strings.Contains(got, agentsSentinelBegin) {
		t.Errorf("the stack worktree kept the block, so a merge brings it back:\n%s", got)
	}

	report := b.String()
	for _, want := range []string{
		stackRepoRoot,
		"committed nothing",
		"nick/feat",
		"a merge brings the block back",
		"devstack stack rm",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the repair report never states %q:\n%s", want, report)
		}
	}

	out := gitOutput(t, stackRepoRoot, "status", "--porcelain", "--", agentsFileName)
	if !strings.HasPrefix(out, " M") {
		t.Errorf("git reports %q for %s, want an unstaged change (\" M\")", out, agentsFileName)
	}
	if n := gitOutput(t, stackRepoRoot, "rev-list", "--count", "nick/feat"); n != "1" {
		t.Errorf("the branch nick/feat is at %s commits, want 1: the repair committed in it", n)
	}
}

// The refusal is the guard that stops the repair destroying work git does not
// hold. A repair runs on every sweep, so a repair without this guard would eat
// an uncommitted edit on any run at all.
func TestTheRepairRefusesAFileThatHoldsAnUncommittedChange(t *testing.T) {
	ws, repoRoot, _ := setupNestedServiceWorkspace(t)
	atCurrentVersion(t, ws)
	writeFile(t, filepath.Join(repoRoot, agentsFileName),
		"# orbit\n\nMine, and it stays.\n\n"+block("Old devstack instructions.\n\nMY UNCOMMITTED EDIT.")+"\n")

	var b strings.Builder
	if err := migrate.Sweep(&b, patches(), []workspace.Workspace{*ws}, true, false); err == nil {
		t.Fatal("Sweep() returned no error, so a caller can not tell that the repair refused")
	}
	if !strings.Contains(readString(t, filepath.Join(repoRoot, agentsFileName)), "MY UNCOMMITTED EDIT") {
		t.Fatal("the repair destroyed the change it refused to destroy")
	}
	if got := b.String(); !strings.Contains(got, "REFUSES to migrate") {
		t.Errorf("the repair never says that it refused:\n%s", got)
	}
}

// --list is the read-only view, and it is what a reader runs before they let
// anything write. It has to name the residue it would remove.
func TestListReportsResidueAtTheCurrentVersionAndChangesNothing(t *testing.T) {
	ws, repoRoot, stackRepoRoot := setupNestedServiceWorkspace(t)
	atCurrentVersion(t, ws)

	var b strings.Builder
	if err := migrate.Sweep(&b, patches(), []workspace.Workspace{*ws}, false, false); err != nil {
		t.Fatalf("Sweep(list) = %v", err)
	}

	got := b.String()
	for _, want := range []string{
		"repair",
		"pending",
		filepath.Join(repoRoot, agentsFileName),
		filepath.Join(stackRepoRoot, agentsFileName),
		"changes nothing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("--list never states %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Do nothing.") {
		t.Errorf("--list tells the reader to do nothing, and residue is pending:\n%s", got)
	}
	if !strings.Contains(readString(t, filepath.Join(repoRoot, agentsFileName)), agentsSentinelBegin) {
		t.Error("--list removed the block, and it must change nothing")
	}
	if len(scanResidue(stackRepoRoot)) != 1 {
		t.Error("--list changed the stack worktree")
	}
}

// The closing line is what an agent acts on. "Do nothing" has to mean that there
// is nothing to do, in the migrations and in the repairs together.
func TestTheReportSaysDoNothingOnlyWhenNoBlockIsLeft(t *testing.T) {
	ws, _, _ := setupNestedServiceWorkspace(t)
	atCurrentVersion(t, ws)

	var first strings.Builder
	if err := migrate.Sweep(&first, patches(), []workspace.Workspace{*ws}, true, false); err != nil {
		t.Fatalf("Sweep() = %v", err)
	}
	if strings.Contains(first.String(), "Do nothing.") {
		t.Errorf("the run removed a block and still said to do nothing:\n%s", first.String())
	}

	var second strings.Builder
	if err := migrate.Sweep(&second, patches(), []workspace.Workspace{*ws}, true, false); err != nil {
		t.Fatalf("second Sweep() = %v", err)
	}
	got := second.String()
	if !strings.Contains(got, "Do nothing.") {
		t.Errorf("no block is left, and the run never says the work is done:\n%s", got)
	}
	if !strings.Contains(got, "every repair is done") {
		t.Errorf("the closing line claims only the migrations, and the repairs also ran:\n%s", got)
	}
}

// The MCP tool and the CLI read one report. An agent that is told to do nothing
// while a block is on the disk acts on a surface that lied to it.
func TestTheMigrateToolReportsResidueAtTheCurrentVersion(t *testing.T) {
	ws, repoRoot, _ := setupNestedServiceWorkspace(t)
	atCurrentVersion(t, ws)

	got := migrateToolCall(t, migrateToolServer(t, ws), map[string]any{"action": "list"})
	if !strings.Contains(got, filepath.Join(repoRoot, agentsFileName)) {
		t.Errorf("the tool never names the residue:\n%s", got)
	}
	if strings.Contains(got, "Do nothing.") {
		t.Errorf("the tool tells an agent to do nothing while a block is on the disk:\n%s", got)
	}
}
