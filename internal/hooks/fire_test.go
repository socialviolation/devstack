package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func TestContextRefusesStackBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	manifest := `version: 1
workspace:
  name: shop
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
`
	service := `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`
	if err := os.WriteFile(filepath.Join(root, config.WorkspaceManifestFileName), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "api"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", config.ServiceManifestFileName), []byte(service), 0644); err != nil {
		t.Fatal(err)
	}
	ws := &workspace.Workspace{Name: "shop", Path: root}

	if _, _, err := Context(ws, "", config.EventWorkspaceUp, nil); err != nil {
		t.Fatalf("Context with no stack: %v", err)
	}
	if _, _, err := Context(ws, "base", config.EventWorkspaceUp, nil); err == nil {
		t.Error("Context with stack base must refuse: base is not a stack")
	}
}
