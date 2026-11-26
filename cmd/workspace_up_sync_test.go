package cmd

import (
	"io"
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

func TestWorkspaceUpMovesAStaleReplicaToTheDefaultBranchTip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	repo := filepath.Join(root, "api")
	gitRepoWith(t, repo, map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	shareServices(t, root, "api")
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	useWorkspaceKey(t, ws.Name)
	var restarted []string
	orig := sharedRestarter
	sharedRestarter = func(io.Writer, string) func(string) (bool, error) {
		return func(svc string) (bool, error) {
			restarted = append(restarted, svc)
			return true, nil
		}
	}
	t.Cleanup(func() { sharedRestarter = orig })

	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("replica.Ensure() = %v", err)
	}
	worktree := filepath.Join(replica.Root(ws), "api")
	before := gitAt(t, worktree, "rev-parse", "HEAD")

	writeAt(t, filepath.Join(repo, "later.txt"), "work that landed on the default branch\n")
	gitAt(t, repo, "add", "-f", ".")
	gitAt(t, repo, "commit", "-q", "-m", "later")
	tip := gitAt(t, repo, "rev-parse", "HEAD")

	if err := syncShared(ws, io.Discard); err != nil {
		t.Fatalf("syncShared() = %v", err)
	}
	if strings.Join(restarted, ",") != "api" {
		t.Errorf("restarted = %v, want the shared service whose repository moved", restarted)
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

func TestWorkspaceUpSyncsTheSharedSetAndThenStartsIt(t *testing.T) {
	body := funcBody(t, "start_cmd.go", "bringWorkspaceUp")

	if !strings.Contains(body, "syncShared(ws, os.Stdout)") {
		t.Error("'workspace up' does not move the replica, so the shared set falls behind the default branch")
	}
	if !strings.Contains(body, "hostdaemon.StartShared(os.Stdout, ws)") {
		t.Error("'workspace up' does not start the shared set")
	}
	if strings.Index(body, "syncShared(ws, os.Stdout)") > strings.Index(body, "hostdaemon.StartShared(os.Stdout, ws)") {
		t.Error("'workspace up' starts the shared set before it moves the replica")
	}
}
