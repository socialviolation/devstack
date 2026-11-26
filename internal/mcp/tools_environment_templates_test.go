package mcp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func TestEnvironmentListsTemplates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: shop
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
groups:
  core: [api]
environments:
  prod-rag:
    values:
      RAG: prod
templates:
  agent:
    purpose: Agent work with prod RAG.
    services: [core]
    env: prod-rag
`)
	writeFile(t, filepath.Join(root, "repos", "api", config.ServiceManifestFileName), basicService)
	ws := &workspace.Workspace{Name: "shop", Path: root}
	s := server.NewMCPServer("test", "0.0.0")
	registerEnvironmentTool(s, "http://localhost:5080", ws.Name, root, "api", ws)

	out := callTool(t, s, "environment", nil)
	if !strings.Contains(out, "templates:") || !strings.Contains(out, "agent: core (env prod-rag) — Agent work with prod RAG.") {
		t.Errorf("environment must list each template with its services, env and purpose; got:\n%s", out)
	}
}
