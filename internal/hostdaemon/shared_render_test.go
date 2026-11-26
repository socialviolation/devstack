package hostdaemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func TestRenderRunsOnlySharedServicesOutsideAStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "dev", "shop")
	makeRepo(t, filepath.Join(root, "db"), "db", 5432)
	writeFile(t, filepath.Join(root, "api", "devstack.service.yaml"),
		"version: 1\nservice:\n  name: api\nruntime:\n  run:\n    command: go run .\nports:\n  http: 8080\nneeds: [db]\n")
	rungit(t, filepath.Join(root, "api"), "init", "-b", "main", "-q")
	rungit(t, filepath.Join(root, "api"), "config", "commit.gpgsign", "false")
	rungit(t, filepath.Join(root, "api"), "add", "-f", ".")
	rungit(t, filepath.Join(root, "api"), "commit", "-q", "-m", "init")
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"),
		"version: 1\nworkspace:\n  name: shop\n  repoDiscovery:\n    mode: explicit\n    repos: [./db, ./api]\nshared: [db]\n")
	if err := workspace.Register(workspace.Workspace{Name: "shop", Path: root, TiltPort: 10350}); err != nil {
		t.Fatal(err)
	}
	if err := workspace.SetWorkspaceActive("shop", true); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.FindByName("shop")
	if err != nil {
		t.Fatal(err)
	}
	unbuilt, _, err := render()
	if err != nil {
		t.Fatalf("render with no replica: %v", err)
	}
	if _, ok := resourceBlocks(unbuilt)["shop:api"]; ok {
		t.Errorf("with no replica, the checkout fallback runs the stack service api: %v", sortedBlockNames(resourceBlocks(unbuilt)))
	}
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	out, _, err := render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	blocks := resourceBlocks(out)
	if _, ok := blocks["shop:db"]; !ok {
		t.Errorf("the shared service shop:db is missing: %v", sortedBlockNames(blocks))
	}
	if _, ok := blocks["shop:api"]; ok {
		t.Errorf("the stack service api runs outside a stack: %v", sortedBlockNames(blocks))
	}

	if _, err := stack.Create(stack.CreateInput{Base: ws, Name: "feat", Repos: []string{"api"}}); err != nil {
		t.Fatalf("stack.Create: %v", err)
	}
	if err := stack.SetActive("shop", "feat", true); err != nil {
		t.Fatal(err)
	}
	out, _, err = render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	blocks = resourceBlocks(out)
	api, ok := blocks["shop:api:feat"]
	if !ok {
		t.Fatalf("the stack resource shop:api:feat is missing: %v", sortedBlockNames(blocks))
	}
	if !strings.Contains(api, `resource_deps=["shop:db"]`) {
		t.Errorf("the stack resource must depend on the shared resource with no stack suffix:\n%s", api)
	}
	if _, ok := blocks["shop:db:feat"]; ok {
		t.Error("the stack runs its own copy of the shared service db")
	}
	if _, ok := blocks["shop:api"]; ok {
		t.Error("api runs outside the stack after the stack came up")
	}
}
