package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

// Whatever creates a thing configures the thing it creates. 'stack create' wires
// each worktree it cuts, and 'workspace add' now does the same for the workspace
// it registers. A workspace that devstack registered must never read as work a
// migration still has to do.
func TestWorkspaceAddLeavesNothingPending(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	registered, err := workspace.FindByPath(root)
	if err != nil {
		t.Fatal(err)
	}

	prepareWorkspace(registered)

	if _, err := os.Stat(filepath.Join(root, claudeSettingsRel)); err != nil {
		t.Errorf("the workspace root holds no SessionStart hook: %v", err)
	}
	if !config.HasWorkspaceManifest(replica.Root(registered)) {
		t.Errorf("no replica at %s", replica.Root(registered))
	}

	st, err := migrate.List(patches(), []workspace.Workspace{*registered})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st {
		if s.Pending() {
			t.Errorf("a workspace devstack has just registered has migration %s pending: %+v", s.ID, s.Rows)
		}
	}
}

// A directory with no manifest is still a workspace somebody wants registered.
// The registration is what the user asked for, and a replica that can not be
// built is a warning and not a refusal.
func TestWorkspaceAddSurvivesAWorkspaceItCanNotBuildAReplicaFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "empty")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	ws := &workspace.Workspace{Name: "empty", Path: root}
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}

	prepareWorkspace(ws)

	if _, err := os.Stat(filepath.Join(root, claudeSettingsRel)); err != nil {
		t.Errorf("the workspace root holds no SessionStart hook: %v", err)
	}
}
