package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A replica that devstack builds one time and never moves falls behind the
// default branch, silently, for as long as the machine is up. That is the whole
// reason the `base` noun existed. 'workspace up' moves it instead.
func TestWorkspaceUpMovesAStaleReplicaToTheDefaultBranchTip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	repo := filepath.Join(root, "api")
	gitRepoWith(t, repo, map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	useWorkspaceKey(t, ws.Name)

	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("replica.Ensure() = %v", err)
	}
	worktree := filepath.Join(replica.Root(ws), "api")
	before := gitAt(t, worktree, "rev-parse", "HEAD")

	writeAt(t, filepath.Join(repo, "later.txt"), "work that landed on the default branch\n")
	gitAt(t, repo, "add", "-f", ".")
	gitAt(t, repo, "commit", "-q", "-m", "later")
	tip := gitAt(t, repo, "rev-parse", "HEAD")

	if err := syncBase(ws); err != nil {
		t.Fatalf("syncBase() = %v", err)
	}

	after := gitAt(t, worktree, "rev-parse", "HEAD")
	if after == before {
		t.Fatalf("the replica stayed at %s, and the default branch is at %s", before, tip)
	}
	if after != tip {
		t.Errorf("the replica is at %s, want the default branch tip %s", after, tip)
	}
	if _, err := os.Stat(filepath.Join(worktree, "later.txt")); err != nil {
		t.Errorf("the replica worktree does not hold the new file: %v", err)
	}
}

// 'workspace up' moves the code under the copies that run, so it must restart
// them. It must restart nothing else: a copy that somebody stopped stays
// stopped, a stack runs its own worktree, and another workspace's replica did
// not move.
func TestWorkspaceUpRestartsOnlyTheRunningBaseCopiesOfThisWorkspace(t *testing.T) {
	view := &tilt.TiltView{UiResources: []tilt.UIResource{
		resourceWith("shop:api", "ok", false),
		resourceWith("shop:web", "none", false),
		resourceWith("shop:worker", "ok", true),
		resourceWith("shop:api:feat", "ok", false),
		resourceWith("other:api", "ok", false),
	}}

	got := strings.Join(runningBaseCopies(view, "shop", nil), ",")
	if got != "shop:api" {
		t.Errorf("runningBaseCopies() = %q, want the one running base copy of shop", got)
	}
}

// The daemon call in the middle puts the whole sequence out of reach of a unit
// test, so the wiring is pinned here. Without the sync, base falls behind the
// default branch. Without the restart, base serves code that is no longer in
// its worktree.
func TestWorkspaceUpSyncsTheReplicaAndThenRestartsWhatRuns(t *testing.T) {
	body := funcBody(t, "start_cmd.go", "bringWorkspaceUp")

	if !strings.Contains(body, "syncBase(ws)") {
		t.Error("'workspace up' does not move the replica, so base falls behind the default branch")
	}
	if !strings.Contains(body, "transformRunningState(os.Stdout, ws.Name, nil)") {
		t.Error("'workspace up' does not restart the copies that run, so they serve code that moved")
	}
	if strings.Index(body, "syncBase(ws)") > strings.Index(body, "transformRunningState") {
		t.Error("'workspace up' restarts the copies before it moves the replica")
	}
}
