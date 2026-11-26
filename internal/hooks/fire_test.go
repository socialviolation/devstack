package hooks

import (
	"os"
	"path/filepath"
	"strings"
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

func TestWorkspaceUpFiresAScopedHookOnlyForSharedServices(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name   string
		shared string
		want   []string
	}{
		{"one shared", "shared: [api]\n", []string{"", "api"}},
		{"none shared", "", []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			manifest := `version: 1
workspace:
  name: shop
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
      - ./web
hooks:
  - name: everywhere
    on: [workspace.up]
    run: "true"
  - name: scoped
    on: [workspace.up]
    services: [api, web]
    run: "true"
` + tc.shared
			if err := os.WriteFile(filepath.Join(root, config.WorkspaceManifestFileName), []byte(manifest), 0644); err != nil {
				t.Fatal(err)
			}
			for _, svc := range []string{"api", "web"} {
				if err := os.MkdirAll(filepath.Join(root, svc), 0755); err != nil {
					t.Fatal(err)
				}
				service := "version: 1\nservice:\n  name: " + svc + "\nruntime:\n  run:\n    command: go run .\n"
				if err := os.WriteFile(filepath.Join(root, svc, config.ServiceManifestFileName), []byte(service), 0644); err != nil {
					t.Fatal(err)
				}
			}
			ws := &workspace.Workspace{Name: "shop", Path: root}

			ev, src, err := Context(ws, "", config.EventWorkspaceUp, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, inv := range Resolve(ev, src) {
				got = append(got, inv.Service)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("workspace.up fires for services %q, want %q", got, tc.want)
			}
		})
	}
}
