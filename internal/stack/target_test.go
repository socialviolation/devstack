package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

// seedTarget registers a base workspace, records a stack with one worktree, and
// creates the replica directory base runs from: the three places a working
// directory can be, and which the resolver has to tell apart.
func seedTarget(t *testing.T) (ws *workspace.Workspace, checkout, worktree, replicaDir string) {
	t.Helper()
	ws = newBase(t)
	checkout = ws.Path

	stackRoot := filepath.Join(filepath.Dir(ws.Path), ".devstack-stacks", "feat")
	worktree = filepath.Join(stackRoot, "backend")
	replicaDir = filepath.Join(replica.Root(ws), "backend")
	for _, dir := range []string{worktree, replicaDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	if err := upsertStack(Record{
		Name:      "feat",
		Base:      ws.Name,
		Root:      stackRoot,
		Worktrees: map[string]string{"backend": worktree},
	}); err != nil {
		t.Fatalf("upsertStack: %v", err)
	}
	return ws, checkout, worktree, replicaDir
}

func TestResolveTarget(t *testing.T) {
	ws, checkout, worktree, replicaDir := seedTarget(t)

	cases := []struct {
		name    string
		flag    string
		dir     string
		want    string
		wantErr bool
	}{
		{name: "explicit stack beats the directory", flag: "feat", dir: replicaDir, want: "feat"},
		{name: "explicit base beats the directory", flag: "base", dir: worktree, want: ""},
		{name: "base is base, not a stack called base", flag: "base", dir: checkout, want: ""},
		{name: "a stack worktree is that stack", dir: worktree, want: "feat"},
		{name: "the replica is base", dir: replicaDir, want: ""},
		{name: "an unknown name is left for the copy lookup to reject", flag: "nope", dir: checkout, want: "nope"},
		{name: "the template checkout is not an implicit base", dir: checkout, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.dir)
			got, err := ResolveTarget(ws, tc.flag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveTarget(%q) = %q, want an error", tc.flag, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTarget(%q): %v", tc.flag, err)
			}
			if got != tc.want {
				t.Errorf("ResolveTarget(%q) in %s = %q, want %q", tc.flag, tc.dir, got, tc.want)
			}
		})
	}
}

// The refusal is only useful if it says what to type instead, so it names base
// and every stack of the workspace.
func TestResolveTargetErrorNamesBaseAndTheStacks(t *testing.T) {
	ws, checkout, _, _ := seedTarget(t)
	t.Chdir(checkout)

	_, err := ResolveTarget(ws, "")
	if err == nil {
		t.Fatal("expected an error in the template checkout")
	}
	for _, want := range []string{"--stack base", "feat"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q, got: %v", want, err)
		}
	}
}

// A workspace with no stacks cannot list any, so the refusal has to say that
// rather than print an empty list.
func TestResolveTargetErrorWithNoStacks(t *testing.T) {
	ws := newBase(t)
	t.Chdir(ws.Path)

	_, err := ResolveTarget(ws, "")
	if err == nil {
		t.Fatal("expected an error in the template checkout")
	}
	if !strings.Contains(err.Error(), "--stack base") || !strings.Contains(err.Error(), "no stacks") {
		t.Errorf("error must offer base and say there are no stacks, got: %v", err)
	}
}

// Another workspace's stack worktree is not this workspace's instance: resolving
// it would send the command to a stack the base does not own.
func TestResolveTargetIgnoresAnotherWorkspacesStack(t *testing.T) {
	ws, _, worktree, _ := seedTarget(t)
	t.Chdir(worktree)

	if _, err := ResolveTarget(&workspace.Workspace{Name: "other", Path: ws.Path}, ""); err == nil {
		t.Error("a stack of workspace navexa must not resolve as a copy of workspace other")
	}
}
