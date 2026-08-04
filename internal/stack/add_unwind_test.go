package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A promotion puts a worktree that somebody owns on the stack's branch. A
// failure after that must put it back: the add reports an error, and the user is
// left with a worktree devstack moved for a stack it did not build.
func TestAddPutsAPromotedWorktreeBackWhenItFailsPartWay(t *testing.T) {
	base := newAddBase(t)
	created := newStack(t, base, "api")

	web := worktreesByService(created)["web"].Path
	if got := gitOut(t, web, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Fatalf("web worktree branch = %q, want a detached HEAD to promote", got)
	}
	head := gitOut(t, web, "rev-parse", "HEAD")

	// startpoint exists in billing and not in worker, so the add fails after it
	// has promoted web.
	git(t, filepath.Join(base.Path, "billing"), "branch", "startpoint")

	_, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"web", "money"}, From: "refs/heads/startpoint"})
	if err == nil {
		t.Fatal("Add with a ref that one repository does not have = nil, want an error")
	}

	if got := gitOut(t, web, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("the failed add left the web worktree on branch %q, want the detached HEAD it started on", got)
	}
	if got := gitOut(t, web, "rev-parse", "HEAD"); got != head {
		t.Errorf("the failed add left the web worktree at %s, want the commit it started on, %s", got, head)
	}
}

// The manifest names the worktrees the stack runs from. A failure after devstack
// wrote it, and before it recorded the stack, leaves a manifest that names
// worktrees the unwind has just removed.
func TestAddPutsTheStackManifestBackWhenItFailsPartWay(t *testing.T) {
	base := newAddBase(t)
	created := newStack(t, base, "api")

	manifestPath := filepath.Join(created.StackRoot, "devstack.workspace.yaml")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read the stack manifest: %v", err)
	}

	// devstack records the stack after it writes the manifest, and it can not
	// write the record in a data directory that is read only.
	data := filepath.Join(os.Getenv("HOME"), ".local", "share", "devstack", "acme")
	if err := os.Chmod(data, 0555); err != nil {
		t.Fatalf("chmod the data directory: %v", err)
	}
	t.Cleanup(func() { os.Chmod(data, 0755) })

	if _, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"billing"}}); err == nil {
		t.Fatal("Add that can not record the stack = nil, want an error")
	}

	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read the stack manifest after the failure: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("the failed add left the stack manifest changed.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(string(after), filepath.Join(created.StackRoot, "billing")) {
		t.Error("the manifest names a worktree that the unwind removed")
	}
}
