package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func writeNeedsWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	svcs := map[string]string{
		"api":     "needs: [db]\ncalls: [billing]\n",
		"db":      "",
		"billing": "",
		"legacy":  "",
	}
	for name, edges := range svcs {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "version: 1\nservice:\n  name: " + name + "\nruntime:\n  run:\n    command: run\n" + edges
		if err := os.WriteFile(filepath.Join(dir, "devstack.service.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws := "version: 1\nworkspace:\n  name: needs\n  repoDiscovery:\n    mode: explicit\n    repos: [./api, ./db, ./billing, ./legacy]\ndependencies:\n  api: [legacy]\n"
	if err := os.WriteFile(filepath.Join(root, "devstack.workspace.yaml"), []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCoreStartOrderFollowsNeedsNotCallsOrTheOldMap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Load(writeNeedsWorkspace(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := coreStartOrder(cfg, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"db", "api"}; !reflect.DeepEqual(got, want) {
		t.Errorf("coreStartOrder(api) = %v, want %v: needs only, never calls or the old dependencies map", got, want)
	}
}
