package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestReadCleanBranch(t *testing.T) {
	dir := repo(t)
	got := Read(dir)
	if got.Branch != "main" || got.Dirty || got.Detached {
		t.Fatalf("Read() = %+v, want branch main, clean, attached", got)
	}
	if got.Label() != "main" {
		t.Errorf("Label() = %q, want %q", got.Label(), "main")
	}
}

// An uncommitted change is what makes a running service's code differ from the
// branch it claims to be on, so it must show in the label.
func TestReadDirtyMarksLabel(t *testing.T) {
	dir := repo(t)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	got := Read(dir)
	if !got.Dirty {
		t.Fatalf("Read() = %+v, want Dirty", got)
	}
	if got.Label() != "main*" {
		t.Errorf("Label() = %q, want %q", got.Label(), "main*")
	}
}

// Untracked files count as uncommitted work.
func TestReadUntrackedIsDirty(t *testing.T) {
	dir := repo(t)
	if err := os.WriteFile(filepath.Join(dir, "new"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := Read(dir); !got.Dirty {
		t.Errorf("Read() = %+v, want Dirty for an untracked file", got)
	}
}

func TestReadDetachedHead(t *testing.T) {
	dir := repo(t)
	cmd := exec.Command("git", "checkout", "--detach", "HEAD")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("detach: %v\n%s", err, out)
	}
	got := Read(dir)
	if !got.Detached {
		t.Fatalf("Read() = %+v, want Detached", got)
	}
	if want := "detached@" + got.Branch; got.Label() != want {
		t.Errorf("Label() = %q, want %q", got.Label(), want)
	}
}

// A service directory that is not a checkout must render blank, not fail.
func TestReadNonRepoIsBlank(t *testing.T) {
	got := Read(t.TempDir())
	if got.Label() != "" {
		t.Errorf("Label() = %q, want empty for a non-repo", got.Label())
	}
	if Read("").Label() != "" {
		t.Errorf("Read(\"\") should be blank")
	}
}

// Several services often share one repo; ReadAll must key results per service
// while reading each directory once.
func TestReadAllKeysByServiceAndFlagsDirty(t *testing.T) {
	clean := repo(t)
	dirty := repo(t)
	if err := os.WriteFile(filepath.Join(dirty, "f"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}

	infos := ReadAll(map[string]string{
		"api":      clean,
		"api-jobs": clean,
		"worker":   dirty,
		"nothing":  "",
	})

	if infos["api"].Branch != "main" || infos["api-jobs"].Branch != "main" {
		t.Errorf("services sharing a repo should both report it: %+v", infos)
	}
	if infos["nothing"].Label() != "" {
		t.Errorf("empty dir should be blank, got %+v", infos["nothing"])
	}
	if got, want := DirtyKeys(infos), []string{"worker"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("DirtyKeys() = %v, want %v", got, want)
	}
}
