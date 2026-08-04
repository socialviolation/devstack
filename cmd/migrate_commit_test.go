package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/workspace"
)

// setupCommittableWorkspace builds a workspace whose service is the root of its
// own git repository, and whose workspace root is in no repository at all. That
// is the normal shape on this machine, and it is the shape the commit
// instruction has to answer for.
func setupCommittableWorkspace(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	svcDir := filepath.Join(root, "api")
	gitRepoWith(t, svcDir, map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	return ws, svcDir
}

// The sweep writes a git diff, and a human commits it. Those are two acts, and
// a session often ends between them. The migration is one-off, so it can not be
// what carries that instruction to the next session. The doctor reports the
// uncommitted diff for as long as it is there, and it fixes nothing.
func TestTheDoctorReportsTheUncommittedDiffTheMigrationLeft(t *testing.T) {
	ws, svcDir := setupCommittableWorkspace(t)

	res, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("runAgentFiles() = %v", err)
	}
	note := strings.Join(nextAgentFiles([]migrate.Result{res}), "\n")
	if !strings.Contains(note, "NOW COMMIT") || !strings.Contains(note, svcDir) {
		t.Fatalf("the run that wrote the diff drops the commit instruction:\n%s", note)
	}

	var b strings.Builder
	if n := reportWorkspaceDrift(&b, ws.Path); n == 0 {
		t.Fatalf("the doctor reports nothing while a devstack file is uncommitted:\n%s", b.String())
	}
	got := b.String()
	if !strings.Contains(got, svcDir) || !strings.Contains(got, commitCommand) {
		t.Errorf("the doctor never names the repository and the command that fixes it:\n%s", got)
	}
	if uncommittedAgentFiles(svcDir) == false {
		t.Fatal("the fixture holds no uncommitted devstack file")
	}

	gitCmd(t, svcDir, "add", "-A")
	gitCmd(t, svcDir, "commit", "-q", "-m", "chore: devstack migrate")

	var after strings.Builder
	reportWorkspaceDrift(&after, ws.Path)
	if strings.Contains(after.String(), "nobody committed") {
		t.Errorf("everything is committed, and the doctor still asks for a commit:\n%s", after.String())
	}
}

// The doctor reports, and it changes nothing. A doctor that wired a repository
// would hide the state it exists to report, and would write in a repository
// nobody asked it to write in.
func TestTheDoctorReportsAnUnconnectedRepositoryAndFixesNothing(t *testing.T) {
	ws, svcDir := setupCommittableWorkspace(t)

	var b strings.Builder
	if n := reportWorkspaceDrift(&b, ws.Path); n == 0 {
		t.Fatalf("the doctor reports nothing for a repository with no .mcp.json:\n%s", b.String())
	}
	got := b.String()
	for _, want := range []string{"not connected", svcDir, "devstack init --all --claude-hook"} {
		if !strings.Contains(got, want) {
			t.Errorf("the doctor never states %q:\n%s", want, got)
		}
	}
	for _, rel := range []string{".mcp.json", claudeSettingsRel} {
		if _, err := os.Stat(filepath.Join(svcDir, rel)); !os.IsNotExist(err) {
			t.Errorf("the doctor wrote %s (stat err = %v)", rel, err)
		}
	}
	if !strings.Contains(got, "no replica") || !strings.Contains(got, "devstack workspace up") {
		t.Errorf("the doctor never reports the missing replica and the command that builds it:\n%s", got)
	}
}

// A run that changes nothing and leaves something to commit must still reach
// the NEXT block. Apply used to drop every result that changed no file, which
// is what bound the instruction to the run instead of to the state.
func TestApplyCarriesAResultThatChangedNothingAndLeftWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "dev", "shop")
	writeAt(t, filepath.Join(root, config.WorkspaceManifestFileName),
		"version: 1\nworkspace:\n  name: shop\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./api\n")

	var b strings.Builder
	p := migrate.Patch{
		From:  1,
		To:    2,
		Title: "test",
		Run: func(ws *workspace.Workspace) (migrate.Result, error) {
			return migrate.Result{Items: []migrate.Item{{Label: "api", Path: "/tmp/api"}}}, nil
		},
		Next: func(res []migrate.Result) []string { return []string{"FINISH THE JOB"} },
	}

	if err := migrate.Apply(&b, []migrate.Patch{p}, []workspace.Workspace{{Name: "shop", Path: root}}, false); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if got := b.String(); !strings.Contains(got, "FINISH THE JOB") {
		t.Errorf("the next action is lost when the run changes no file:\n%s", got)
	}
}

// `git add -A && git commit` in a directory that is not a repository root exits
// 128 outside every repository, and stages an unrelated repository below one.
// Neither is a thing to print beside a path and call an instruction.
func TestTheNoteNeverAsksForACommitOutsideARepositoryRoot(t *testing.T) {
	home := t.TempDir()
	loose := filepath.Join(home, "dev", "shop")
	writeAt(t, filepath.Join(loose, claudeSettingsRel), "{}\n")

	outer := filepath.Join(home, "dotfiles")
	inner := filepath.Join(outer, "dev", "shop")
	gitRepoWith(t, outer, map[string]string{".": "dots"})
	writeAt(t, filepath.Join(inner, claudeSettingsRel), "{}\n")

	got := strings.Join(nextAgentFiles([]migrate.Result{{
		Workspace: "shop",
		Items: []migrate.Item{
			{Label: "workspace root", Path: loose},
			{Label: "workspace root", Path: inner},
		},
	}}), "\n")

	if strings.Contains(got, "NOW COMMIT") {
		t.Errorf("neither directory can be committed in:\n%s", got)
	}
	for _, want := range []string{
		"COMMIT THESE ELSEWHERE",
		"No git repository holds this directory",
		claudeSettingsRel,
		"in the repository " + outer,
		"stages that whole repository",
		"git -C " + outer + " add " + filepath.Join("dev", "shop", claudeSettingsRel),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the note never states %q:\n%s", want, got)
		}
	}
}

// The workspace root of the machines devstack runs on is not a repository, and
// it was the first line of the list that told a reader to run git add -A.
func TestTheWorkspaceRootIsNeverListedAsARepositoryToCommitIn(t *testing.T) {
	ws, svcDir := setupCommittableWorkspace(t)

	res, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("runAgentFiles() = %v", err)
	}
	got := strings.Join(nextAgentFiles([]migrate.Result{res}), "\n")

	commit, _, _ := strings.Cut(got, "COMMIT THESE ELSEWHERE")
	if strings.Contains(commit, ws.Path+"\n") {
		t.Errorf("the workspace root %s is not a git repository, so it is not somewhere to commit:\n%s", ws.Path, got)
	}
	if !strings.Contains(commit, svcDir) {
		t.Errorf("the service repository is the one place a commit belongs:\n%s", got)
	}
	if !strings.Contains(got, "No git repository holds this directory") {
		t.Errorf("the note never says what happens to the file in the workspace root:\n%s", got)
	}
}

// The sweep writes .mcp.json into the repository the agent sits in, and an MCP
// client reads its server list once, at session start. Nothing else in this
// output says that the session that ran the sweep does not have those tools.
func TestTheNoteTellsTheSessionToRestartForTheTools(t *testing.T) {
	ws, _ := setupCommittableWorkspace(t)
	res, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("runAgentFiles() = %v", err)
	}

	got := strings.Join(nextAgentFiles([]migrate.Result{res}), "\n")

	for _, want := range []string{".mcp.json", "session start", "restart the session"} {
		if !strings.Contains(got, want) {
			t.Errorf("the note never states %q:\n%s", want, got)
		}
	}
}

// The detector reads the filesystem and reaches no network, because it runs on
// every migrate and on every upgrade report.
func TestTheUncommittedCheckAnswersFalseOutsideARepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if uncommittedAgentFiles(dir) {
		t.Error("no repository holds this directory, so no commit is pending in it")
	}
}

// `base` is the repair for a replica, and the replica report named only
// `workspace up`. Somebody reading this line had no way to reach the command
// that builds a replica and restarts nothing, or the help that says what a
// replica is.
func TestTheDoctorNamesBaseInTheReplicaReport(t *testing.T) {
	ws, _ := setupCommittableWorkspace(t)

	var b strings.Builder
	reportWorkspaceDrift(&b, ws.Path)
	got := b.String()

	if !strings.Contains(got, "no replica") {
		t.Fatalf("the fixture has a replica, so this checks nothing:\n%s", got)
	}
	for _, want := range []string{"devstack workspace up", "devstack base sync", "devstack base --help"} {
		if !strings.Contains(got, want) {
			t.Errorf("the replica report never names %q:\n%s", want, got)
		}
	}
}
