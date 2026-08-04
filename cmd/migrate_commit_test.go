package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
// a session often ends between them. An instruction that prints only on the run
// that wrote the diff is lost, and the diff stays uncommitted for ever, so the
// state of the disk prints it instead.
func TestTheCommitInstructionOutlivesTheRunThatWroteTheDiff(t *testing.T) {
	ws, svcDir := setupCommittableWorkspace(t)

	if _, err := runAgentFiles(ws); err != nil {
		t.Fatalf("runAgentFiles() = %v", err)
	}

	pending, why, err := detectAgentFiles(ws)
	if err != nil {
		t.Fatalf("detectAgentFiles() = %v", err)
	}
	if !pending {
		t.Fatalf("the diff is uncommitted, so the patch still has work: %q", why)
	}
	if !strings.Contains(why, "uncommitted") {
		t.Errorf("the detector says %q, which never mentions the uncommitted diff", why)
	}

	second, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("second runAgentFiles() = %v", err)
	}
	if second.Changed {
		t.Errorf("the second run must change no file:\n%s", strings.Join(second.Lines, "\n"))
	}
	note := strings.Join(nextAgentFiles([]migrate.Result{second}), "\n")
	if !strings.Contains(note, "NOW COMMIT") || !strings.Contains(note, svcDir) {
		t.Errorf("the second run drops the commit instruction:\n%s", note)
	}

	gitCmd(t, svcDir, "add", "-A")
	gitCmd(t, svcDir, "commit", "-q", "-m", "chore: devstack migrate")

	pending, why, err = detectAgentFiles(ws)
	if err != nil {
		t.Fatalf("detectAgentFiles() = %v", err)
	}
	if pending {
		t.Errorf("everything is committed, so the patch asks for nothing: %q", why)
	}
	third, err := runAgentFiles(ws)
	if err != nil {
		t.Fatalf("third runAgentFiles() = %v", err)
	}
	if len(third.Items) != 0 {
		t.Errorf("a committed repository must not be listed again: %+v", third.Items)
	}
}

// A run that changes nothing and leaves something to commit must still reach
// the NEXT block. Apply used to drop every result that changed no file, which
// is what bound the instruction to the run instead of to the state.
func TestApplyCarriesAResultThatChangedNothingAndLeftWork(t *testing.T) {
	var b strings.Builder
	p := migrate.Patch{
		ID:     "test-patch",
		Title:  "test",
		Rescan: true,
		Detect: func(*workspace.Workspace) (bool, string, error) { return true, "", nil },
		Run: func(ws *workspace.Workspace) (migrate.Result, error) {
			return migrate.Result{Items: []migrate.Item{{Label: "api", Path: "/tmp/api"}}}, nil
		},
		Next: func(res []migrate.Result) []string { return []string{"FINISH THE JOB"} },
	}

	if err := migrate.Apply(&b, []migrate.Patch{p}, []workspace.Workspace{{Name: "shop"}}); err != nil {
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
