package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/workspace"
)

const (
	testDSNPassword = "hunter2"
	testDSN         = "Server=db.example.com;Database=appdb;User Id=svc;Password=" + testDSNPassword
	testOpaqueToken = "abcdef0123456789zz"
)

// newEnvToolWorkspace lays down a workspace whose active environment carries a
// structured connection string, an opaque token and a plain value.
func newEnvToolWorkspace(t *testing.T) (*server.MCPServer, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws
  env: dev
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
environments:
  dev:
    values:
      ConnectionStrings__App: `+testDSN+`
      API_TOKEN: `+testOpaqueToken+`
      LOG_LEVEL: debug
`)
	writeFile(t, filepath.Join(root, "repos", "api", config.ServiceManifestFileName), basicService)

	ws := &workspace.Workspace{Name: "testws", Path: root}
	s := server.NewMCPServer("test", "0.0.0")
	registerEnvTools(s, ws, root)
	return s, root
}

func callTool(t *testing.T, s *server.MCPServer, name string, args map[string]string) string {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp := s.HandleMessage(context.Background(), raw)
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A connection string must stay legible — an agent has to be able to tell which
// database a service points at — while the password does not survive.
func TestEnvWhichRedactsCredentialInPlace(t *testing.T) {
	s, _ := newEnvToolWorkspace(t)
	out := callTool(t, s, "env_which", map[string]string{"service": "api"})

	if strings.Contains(out, testDSNPassword) {
		t.Errorf("env_which leaked the password: %s", out)
	}
	for _, want := range []string{"db.example.com", "appdb", "User Id=svc"} {
		if !strings.Contains(out, want) {
			t.Errorf("env_which dropped identifying part %q: %s", want, out)
		}
	}
}

// A secret with no structure to preserve must still be masked whole.
func TestEnvWhichMasksOpaqueSecret(t *testing.T) {
	s, _ := newEnvToolWorkspace(t)
	out := callTool(t, s, "env_which", map[string]string{"service": "api"})

	if strings.Contains(out, testOpaqueToken) {
		t.Errorf("env_which leaked an opaque token: %s", out)
	}
	if !strings.Contains(out, svcconfig.MaskedValue) {
		t.Errorf("env_which did not mask the opaque token: %s", out)
	}
}

func TestEnvWhichRefusesStackBase(t *testing.T) {
	s, _ := newEnvToolWorkspace(t)
	out := callTool(t, s, "env_which", map[string]string{"service": "api", "stack": "base"})
	if !strings.Contains(out, `"isError":true`) {
		t.Errorf("stack=\"base\" names no stack, and env_which must refuse it:\n%s", out)
	}
}

func TestEnvUseScopeWorkspaceSetsTheWorkspaceDefault(t *testing.T) {
	s, root := newEnvToolWorkspace(t)
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
environments:
  dev:
    values:
      LOG_LEVEL: debug
`)
	out := callTool(t, s, "env_use", map[string]string{"name": "dev", "scope": "workspace"})
	if !strings.Contains(out, `workspace \"testws\" now uses env \"dev\"`) {
		t.Fatalf("scope=workspace must set the workspace default:\n%s", out)
	}
	m, err := config.LoadWorkspaceManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Workspace.Env != "dev" {
		t.Errorf("workspace.env = %q, want dev", m.Workspace.Env)
	}
}

func TestEnvUseRefusesBadScopes(t *testing.T) {
	s, _ := newEnvToolWorkspace(t)
	for name, args := range map[string]map[string]string{
		"scope and stack":   {"name": "dev", "scope": "workspace", "stack": "feat"},
		"scope and service": {"name": "dev", "scope": "workspace", "service": "api"},
		"unknown scope":     {"name": "dev", "scope": "stack"},
		"stack base":        {"name": "dev", "stack": "base"},
	} {
		if out := callTool(t, s, "env_use", args); !strings.Contains(out, `"isError":true`) {
			t.Errorf("%s: env_use must refuse:\n%s", name, out)
		}
	}
}

func TestEnvWhichLeavesNonSecretAlone(t *testing.T) {
	s, _ := newEnvToolWorkspace(t)
	out := callTool(t, s, "env_which", map[string]string{"service": "api"})

	if !strings.Contains(out, "LOG_LEVEL") || !strings.Contains(out, "debug") {
		t.Errorf("env_which altered a non-secret value: %s", out)
	}
}

// The set confirmation goes through the same redaction as the listing.
func TestEnvSetRedactsConfirmation(t *testing.T) {
	s, _ := newEnvToolWorkspace(t)
	out := callTool(t, s, "env_set", map[string]string{
		"name":  "dev",
		"key":   "ConnectionStrings__Other",
		"value": testDSN,
	})

	if strings.Contains(out, testDSNPassword) {
		t.Errorf("env_set leaked the password: %s", out)
	}
	if !strings.Contains(out, "db.example.com") {
		t.Errorf("env_set dropped the server: %s", out)
	}
}

func TestEnvSetMasksOpaqueSecret(t *testing.T) {
	s, _ := newEnvToolWorkspace(t)
	out := callTool(t, s, "env_set", map[string]string{
		"name":  "dev",
		"key":   "OTHER_TOKEN",
		"value": testOpaqueToken,
	})

	if strings.Contains(out, testOpaqueToken) {
		t.Errorf("env_set leaked an opaque token: %s", out)
	}
	if !strings.Contains(out, svcconfig.MaskedValue) {
		t.Errorf("env_set did not mask the opaque token: %s", out)
	}
}
