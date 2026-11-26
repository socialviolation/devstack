package stack

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeStartOrderRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, edges := range map[string]string{
		"api":     "needs: [db]\ncalls: [billing]\n",
		"db":      "",
		"billing": "",
		"legacy":  "",
	} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "version: 1\nservice:\n  name: " + name + "\nruntime:\n  run:\n    command: run\n" + edges
		if err := os.WriteFile(filepath.Join(dir, "devstack.service.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws := "version: 1\nworkspace:\n  name: order\n  repoDiscovery:\n    mode: explicit\n    repos: [./api, ./db, ./billing, ./legacy]\ndependencies:\n  api: [legacy]\n"
	if err := os.WriteFile(filepath.Join(root, "devstack.workspace.yaml"), []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStartOrderFollowsNeedsNotCallsOrTheOldMap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rec := &Record{Name: "feat", Root: writeStartOrderRoot(t), Members: []string{"legacy", "billing", "api", "db"}}
	if got, want := StartOrder(rec), []string{"db", "api", "billing", "legacy"}; !reflect.DeepEqual(got, want) {
		t.Errorf("StartOrder = %v, want %v: needs first, never calls or the old dependencies map", got, want)
	}
}

func TestStartOrderListsOnlyMembers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rec := &Record{Name: "feat", Root: writeStartOrderRoot(t), Members: []string{"api"}}
	if got, want := StartOrder(rec), []string{"api"}; !reflect.DeepEqual(got, want) {
		t.Errorf("StartOrder = %v, want %v: a needed service outside the stack is not in its namespace", got, want)
	}
}
