package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

const nestedSvc = `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`

// A workspace registered at an ancestor path contains the stack roots and the
// replica of every workspace nested under it, so a prefix match finds it there
// too — and it is the only one of the two the registry knows about. Resolving to
// it means a stack worktree reports another workspace's stacks, and --stack base
// in it starts and stops another workspace's services.
func TestNestedWorkspaceDoesNotHijackAStackWorktree(t *testing.T) {
	outer, inner, worktree, _ := nestedLayout(t)
	t.Chdir(worktree)

	ws, err := resolveWorkspace("")
	if err != nil {
		t.Fatalf("resolveWorkspace: %v", err)
	}
	if ws.Name != inner {
		t.Errorf("workspace = %q, want %q — the stack's own workspace, not the one registered at %s", ws.Name, inner, outer)
	}
}

func TestNestedWorkspaceDoesNotHijackTheReplica(t *testing.T) {
	outer, inner, _, replicaDir := nestedLayout(t)
	t.Chdir(replicaDir)

	ws, err := resolveWorkspace("")
	if err != nil {
		t.Fatalf("resolveWorkspace: %v", err)
	}
	if ws.Name != inner {
		t.Errorf("workspace = %q, want %q — the replica's own workspace, not the one registered at %s", ws.Name, inner, outer)
	}
}

// The ancestor still wins where it genuinely owns the directory: its own
// checkout must not start resolving to the workspace nested inside it.
func TestNestedWorkspaceStillResolvesItsOwnCheckout(t *testing.T) {
	_, _, _, _ = nestedLayout(t)
	outerWS, err := workspace.FindByName("outer")
	if err != nil {
		t.Fatalf("find outer: %v", err)
	}
	t.Chdir(filepath.Join(outerWS.Path, "web"))

	ws, err := resolveWorkspace("")
	if err != nil {
		t.Fatalf("resolveWorkspace: %v", err)
	}
	if ws.Name != "outer" {
		t.Errorf("workspace = %q, want outer", ws.Name)
	}
}

func writeNested(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// nestedLayout registers "outer" at a directory that contains "inner", so
// inner's stack root and replica root both sit inside outer's tree.
func nestedLayout(t *testing.T) (outer, inner, worktree, replicaDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	outerPath := filepath.Join(home, "dev", "tsfc")
	innerPath := filepath.Join(outerPath, "devstack_test")

	writeNested(t, filepath.Join(outerPath, "web", config.ServiceManifestFileName), nestedSvc)
	writeNested(t, filepath.Join(outerPath, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: outer
  repoDiscovery:
    mode: explicit
    repos:
      - ./web
`)
	writeNested(t, filepath.Join(innerPath, "api", config.ServiceManifestFileName), nestedSvc)
	writeNested(t, filepath.Join(innerPath, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: inner
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
`)

	for _, ws := range []workspace.Workspace{
		{Name: "outer", Path: outerPath},
		{Name: "inner", Path: innerPath},
	} {
		if err := workspace.Register(ws); err != nil {
			t.Fatalf("register %s: %v", ws.Name, err)
		}
	}

	worktree = filepath.Join(outerPath, ".devstack-stacks", "feat", "api")
	writeNested(t, filepath.Join(worktree, config.ServiceManifestFileName), nestedSvc)
	rec := stack.Record{
		Name:      "feat",
		Base:      "inner",
		Root:      filepath.Join(outerPath, ".devstack-stacks", "feat"),
		Worktrees: map[string]string{"api": worktree},
	}
	data, err := json.Marshal([]stack.Record{rec})
	if err != nil {
		t.Fatal(err)
	}
	storeDir := workspace.DataDir("inner")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "stacks.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	replicaDir = filepath.Join(outerPath, ".devstack-base", "inner", "api")
	writeNested(t, filepath.Join(replicaDir, config.ServiceManifestFileName), nestedSvc)

	return "outer", "inner", worktree, replicaDir
}
