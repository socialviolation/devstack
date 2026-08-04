package migrate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

// A registry devstack can not read is not an empty machine. Reported as one, the
// sweep says "there is nothing to migrate" and exits with success, and the
// upgrade that called it reports a machine it never touched as done.
func TestWorkspacesReportsARegistryItCanNotRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	write(t, workspace.RegistryPath(), "{ this is not the registry")

	all, err := Workspaces()
	if err == nil {
		t.Fatalf("Workspaces() = %v, nil; want the parse failure", all)
	}
	if len(all) != 0 {
		t.Errorf("Workspaces() returned %d workspaces beside the error", len(all))
	}
	if !strings.Contains(err.Error(), "registry") {
		t.Errorf("the error does not name the registry: %v", err)
	}
}

func TestWorkspacesReadsAnEmptyMachineAsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	all, err := Workspaces()
	if err != nil {
		t.Fatalf("Workspaces() = %v, want no error on a machine with no registry", err)
	}
	if len(all) != 0 {
		t.Errorf("Workspaces() = %v, want none", all)
	}
}

func TestWorkspacesSortsByName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	write(t, workspace.RegistryPath(), `[{"name":"shop","path":"`+filepath.Join(home, "shop")+`"},{"name":"navexa","path":"`+filepath.Join(home, "navexa")+`"}]`)

	all, err := Workspaces()
	if err != nil {
		t.Fatalf("Workspaces() = %v", err)
	}
	if len(all) != 2 || all[0].Name != "navexa" || all[1].Name != "shop" {
		t.Errorf("Workspaces() = %v, want them in name order", all)
	}
}
