package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRemoteStateReadsBehindAndGone(t *testing.T) {
	root := t.TempDir()
	origin, work, other := filepath.Join(root, "origin"), filepath.Join(root, "work"), filepath.Join(root, "other")
	run(t, root, "init", "--bare", "-b", "main", origin)
	run(t, root, "clone", origin, work)
	run(t, work, "checkout", "-b", "feat")
	run(t, work, "commit", "--allow-empty", "-m", "one")
	run(t, work, "push", "-u", "origin", "feat")

	if st, err := remoteState(work, "feat"); err != nil || st.Behind || st.Gone {
		t.Fatalf("in step: %+v, %v", st, err)
	}
	if st, _ := remoteState(work, "other-branch"); st.Repo != "" {
		t.Errorf("a worktree off the stack branch was checked: %+v", st)
	}

	run(t, root, "clone", "-b", "feat", origin, other)
	run(t, other, "commit", "--allow-empty", "-m", "two")
	run(t, other, "push", "origin", "feat")
	if st, err := remoteState(work, "feat"); err != nil || !st.Behind {
		t.Errorf("behind: %+v, %v", st, err)
	}

	run(t, other, "push", "origin", "--delete", "feat")
	if st, err := remoteState(work, "feat"); err != nil || !st.Gone {
		t.Errorf("gone: %+v, %v", st, err)
	}
}

func TestListWorktreesMarksPrunable(t *testing.T) {
	root := t.TempDir()
	repo, wt := filepath.Join(root, "repo"), filepath.Join(root, "wt")
	run(t, root, "init", "-b", "main", repo)
	run(t, repo, "commit", "--allow-empty", "-m", "one")
	run(t, repo, "worktree", "add", "--detach", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	wts, err := listWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 2 || wts[0].Prunable || !wts[1].Prunable {
		t.Errorf("got %+v", wts)
	}
}
