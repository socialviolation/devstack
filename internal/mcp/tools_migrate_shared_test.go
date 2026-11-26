package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func sharedStacksServer(t *testing.T) (*server.MCPServer, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	manifest := filepath.Join(root, config.WorkspaceManifestFileName)
	writeFile(t, manifest, "version: 2\nworkspace:\n  name: shop\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./pg\n")
	writeFile(t, filepath.Join(root, "pg", config.ServiceManifestFileName), "version: 1\nservice:\n  name: pg\nruntime:\n  run:\n    command: docker compose up pg\n")
	ws := &workspace.Workspace{Name: "shop", Path: root}
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerMigrateTool(s, ws, nil)
	return s, manifest
}

func TestMigrateSharedStacksDryRunChangesNothing(t *testing.T) {
	s, manifest := sharedStacksServer(t)
	before, _ := os.ReadFile(manifest)

	out := callAs(t, s, owner, "migrate", `{"action":"shared-stacks"}`)
	if strings.Contains(out, `"isError":true`) || !strings.Contains(out, "DRY RUN") {
		t.Fatalf("shared-stacks without apply: want a dry run, got %s", out)
	}
	after, _ := os.ReadFile(manifest)
	if string(before) != string(after) {
		t.Fatalf("the dry run changed the manifest:\n%s", after)
	}
}

func TestMigrateSharedStacksApplyWritesTheSharedList(t *testing.T) {
	s, manifest := sharedStacksServer(t)

	out := callAs(t, s, owner, "migrate", `{"action":"shared-stacks","apply":true,"shared":"pg"}`)
	if strings.Contains(out, `"isError":true`) {
		t.Fatalf("shared-stacks apply: %s", out)
	}
	data, _ := os.ReadFile(manifest)
	if !strings.Contains(string(data), "shared:") || !strings.Contains(string(data), "pg") {
		t.Fatalf("apply did not write the shared list:\n%s", data)
	}
}

func TestMigrateSharedStacksIsDeniedToAnAgent(t *testing.T) {
	s, manifest := sharedStacksServer(t)
	before, _ := os.ReadFile(manifest)

	out := callAs(t, s, alice, "migrate", `{"action":"shared-stacks","apply":true,"shared":"pg"}`)
	if !strings.Contains(out, `"isError":true`) {
		t.Fatalf("an agent ran shared-stacks apply: %s", out)
	}
	after, _ := os.ReadFile(manifest)
	if string(before) != string(after) {
		t.Fatalf("an agent call changed the manifest:\n%s", after)
	}
}
