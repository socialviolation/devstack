package stack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

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
		{name: "the template checkout falls back to base", dir: checkout, want: ""},
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

func TestResolveTargetWithNoStacksIsBase(t *testing.T) {
	ws := newBase(t)
	t.Chdir(ws.Path)

	got, err := ResolveTarget(ws, "")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveTarget in a workspace with no stacks = %q, want base", got)
	}
}

func TestResolveTargetIgnoresAnotherWorkspacesStack(t *testing.T) {
	ws, _, worktree, _ := seedTarget(t)
	t.Chdir(worktree)

	got, err := ResolveTarget(&workspace.Workspace{Name: "other", Path: ws.Path}, "")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if got != "" {
		t.Errorf("a stack of workspace navexa resolved as %q for workspace other, want base", got)
	}
}
