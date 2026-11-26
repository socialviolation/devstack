package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/urls"
	"github.com/socialviolation/devstack/internal/workspace"
)

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestTakeWorkspaceListsOnlyTheSharedSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeAt(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: shop
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
      - ./db
groups:
  core: [api, db]
shared: [db]
`)
	for _, name := range []string{"api", "db"} {
		writeAt(t, filepath.Join(root, name, "devstack.service.yaml"), "version: 1\nservice:\n  name: "+name+"\nruntime:\n  run:\n    command: run\n")
	}

	got := takeWorkspace(&workspace.Workspace{Name: "shop", Path: root}, &tilt.TiltView{}, urls.Map{})
	if len(got.Shared) != 1 || got.Shared[0].Name != "db" {
		t.Errorf("shared rows = %+v, want only db: api runs only in a stack", got.Shared)
	}
}

func TestServiceCommandNamesAStackOnlyForAStackRow(t *testing.T) {
	shared := serviceCommand("restart", row{workspace: "shop", service: Service{Name: "db"}})
	for _, a := range shared {
		if a == "--stack" {
			t.Errorf("a shared row must not pass --stack: %v", shared)
		}
	}
	inStack := serviceCommand("restart", row{workspace: "shop", stack: "feat", service: Service{Name: "api"}})
	want := []string{"service", "restart", "api", "--stack", "feat", "--workspace", "shop"}
	if len(inStack) != len(want) {
		t.Fatalf("stack row = %v, want %v", inStack, want)
	}
	for i := range want {
		if inStack[i] != want[i] {
			t.Fatalf("stack row = %v, want %v", inStack, want)
		}
	}
}
