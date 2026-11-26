package cmd

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tilt"
)

func TestStatusListsTheSharedSetAndNoStackService(t *testing.T) {
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
	rw, err := config.ResolveWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	names := sharedStatusNames(rw, map[string]tilt.UIResource{"cache": {}})
	got := make([]string, 0, len(names))
	for n := range names {
		got = append(got, n)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "cache" || got[1] != "db" {
		t.Errorf("shared rows = %v, want [cache db]: api runs only in a stack", got)
	}
}
