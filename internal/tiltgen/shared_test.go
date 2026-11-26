package tiltgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func TestGenerateHostDependsAStackResourceOnTheSharedResourceWithNoSuffix(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, config.WorkspaceManifestFileName), "version: 1\nworkspace:\n  name: shop\n  repoDiscovery:\n    mode: explicit\n    repos: [./db]\nshared: [db]\n")
	write(t, filepath.Join(base, "db", config.ServiceManifestFileName), "version: 1\nservice:\n  name: db\nruntime:\n  run:\n    command: run-db\n")
	baseRW, err := config.ResolveWorkspace(base)
	if err != nil {
		t.Fatal(err)
	}

	stackRoot := t.TempDir()
	write(t, filepath.Join(stackRoot, config.WorkspaceManifestFileName), "version: 1\nworkspace:\n  name: shop--feat\n  repoDiscovery:\n    mode: explicit\n    repos: [./api, ./web]\npartial: true\n")
	write(t, filepath.Join(stackRoot, "api", config.ServiceManifestFileName), "version: 1\nservice:\n  name: api\nruntime:\n  run:\n    command: run-api\nneeds: [db]\n")
	write(t, filepath.Join(stackRoot, "web", config.ServiceManifestFileName), "version: 1\nservice:\n  name: web\nruntime:\n  run:\n    command: run-web\nneeds: [api, db]\n")
	stackRW, err := config.ResolveWorkspace(stackRoot)
	if err != nil {
		t.Fatal(err)
	}

	out, _, err := GenerateHost([]WorkspaceGen{{
		Name:   "shop",
		Base:   baseRW,
		Stacks: []StackGen{{Workspace: stackRW, Namespace: "feat", Options: Options{Shared: map[string]bool{"db": true}}}},
	}})
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}
	if !strings.Contains(out, `resource_deps=["shop:db"]`) {
		t.Errorf("api must depend on shop:db with no stack suffix:\n%s", out)
	}
	if !strings.Contains(out, `resource_deps=["shop:api:feat", "shop:db"]`) {
		t.Errorf("web must keep the suffix on the member it needs and drop it on the shared one:\n%s", out)
	}
	if strings.Contains(out, "shop:db:feat") {
		t.Errorf("a dep on a shared service must never name a stack copy:\n%s", out)
	}
}
