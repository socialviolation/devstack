package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func repoOn(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-m", "init")
	return dir
}

// A clone whose origin/HEAD names trunk must resolve to trunk even though a
// local main also exists: the remote's opinion is the authoritative one.
func TestDefaultBranchFromOriginHead(t *testing.T) {
	src := repoOn(t, "trunk")
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")
	run(t, dir, "clone", "--bare", src, origin)
	run(t, dir, "clone", origin, work)
	run(t, work, "branch", "main")

	got, err := DefaultBranch(work)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "trunk" {
		t.Errorf("DefaultBranch = %q, want trunk from origin/HEAD", got)
	}
}

func TestDefaultBranchLocalFallback(t *testing.T) {
	cases := []struct {
		name   string
		branch string
		want   string
	}{
		{name: "main", branch: "main", want: "main"},
		{name: "master", branch: "master", want: "master"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DefaultBranch(repoOn(t, tc.branch))
			if err != nil {
				t.Fatalf("DefaultBranch: %v", err)
			}
			if got != tc.want {
				t.Errorf("DefaultBranch = %q, want %q", got, tc.want)
			}
		})
	}
}

// main beats master when a repo carries both, so the answer never depends on
// which one git happens to list first.
func TestDefaultBranchPrefersMainOverMaster(t *testing.T) {
	dir := repoOn(t, "master")
	run(t, dir, "branch", "main")

	got, err := DefaultBranch(dir)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("DefaultBranch = %q, want main", got)
	}
}

func TestDefaultBranchUnknown(t *testing.T) {
	dir := repoOn(t, "trunk")

	_, err := DefaultBranch(dir)
	if err == nil {
		t.Fatal("DefaultBranch = nil error for a repo with no origin and no main/master")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name the directory %q, got: %v", dir, err)
	}
}

func TestDefaultBranchNonRepo(t *testing.T) {
	if _, err := DefaultBranch(t.TempDir()); err == nil {
		t.Fatal("DefaultBranch = nil error for a directory that is not a checkout")
	}
}
