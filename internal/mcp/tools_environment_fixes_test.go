package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	_ "github.com/socialviolation/devstack/internal/otel/plugins/openobserve"
	"github.com/socialviolation/devstack/internal/workspace"
)

// newEnvironmentToolWorkspace lays down a two-service workspace with one feature
// stack in flight and observability on.
func newEnvironmentToolWorkspace(t *testing.T) *server.MCPServer {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  env: dev
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
      - ./repos/worker
observability:
  enabled: true
environments:
  dev:
    values:
      LOG_LEVEL: debug
`)
	writeFile(t, filepath.Join(root, "repos", "api", config.ServiceManifestFileName), basicService)
	writeFile(t, filepath.Join(root, "repos", "worker", config.ServiceManifestFileName), `version: 1
service:
  name: worker
runtime:
  run:
    command: go run .
`)

	writeStacksStore(t, "navexa", `[
	  {"name":"perf","base":"navexa","root":"/x/perf","branch":"perf","active":true,
	   "overlay":["api"],"worktrees":{"api":"/x/perf/api"},
	   "ports":{"api/http":20000},"created_at":"2026-07-21T00:00:00Z"}
	]`)

	ws := &workspace.Workspace{Name: "navexa", Path: root}
	s := server.NewMCPServer("test", "0.0.0")
	registerEnvironmentTool(s, "http://localhost:5080", ws.Name, root, "api", ws)
	return s
}

// Orientation must hand the agent the vocabulary every other tool demands:
// exact service names, the default service, and stack short names.
func TestEnvironmentNamesServicesAndStackShortNames(t *testing.T) {
	s := newEnvironmentToolWorkspace(t)
	out := callTool(t, s, "environment", nil)

	for _, want := range []string{"services: api, worker", "default: api", "perf (active"} {
		if !strings.Contains(out, want) {
			t.Errorf("environment output must contain %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "navexa--perf ") {
		t.Errorf("stacks must be listed by the short name tools take, not the full identity; got:\n%s", out)
	}
}

// The scoping defaults are the commonest source of wrong conclusions, so
// orientation states them where telemetry can be queried at all.
func TestEnvironmentStatesQueryScoping(t *testing.T) {
	s := newEnvironmentToolWorkspace(t)
	out := callTool(t, s, "environment", nil)

	for _, want := range []string{"query scope:", "stay inside this workspace", "defaults to the base instance", "attribute search has no default service"} {
		if !strings.Contains(out, want) {
			t.Errorf("environment output must contain %q; got:\n%s", want, out)
		}
	}
}

// environment reads; nothing it does can change the workspace.
func TestEnvironmentToolAnnotatedReadOnly(t *testing.T) {
	s := newEnvironmentToolWorkspace(t)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					ReadOnlyHint    *bool `json:"readOnlyHint"`
					DestructiveHint *bool `json:"destructiveHint"`
					IdempotentHint  *bool `json:"idempotentHint"`
					OpenWorldHint   *bool `json:"openWorldHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		t.Fatal(err)
	}

	for _, tool := range listing.Result.Tools {
		if tool.Name != "environment" {
			continue
		}
		a := tool.Annotations
		if a.ReadOnlyHint == nil || !*a.ReadOnlyHint {
			t.Error("environment must be annotated readOnlyHint=true")
		}
		if a.DestructiveHint == nil || *a.DestructiveHint {
			t.Error("environment must be annotated destructiveHint=false")
		}
		if a.IdempotentHint == nil || !*a.IdempotentHint {
			t.Error("environment must be annotated idempotentHint=true")
		}
		if a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Error("environment must be annotated openWorldHint=false")
		}
		return
	}
	t.Fatalf("environment tool not listed: %s", data)
}

// The orientation list is maintained by hand, so it drifts the moment a tool is
// added. An agent that trusts it then never calls the tool it needed.
func TestEnvironmentToolListMatchesRegisteredTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	mustWriteColdFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos: []
observability:
  enabled: true
`)
	ws := &workspace.Workspace{Name: "navexa", Path: root}

	s := server.NewMCPServer("test", "0.0.0")
	RegisterTools(s, nil, "", nil, ws.Name, ws.Path, ws)
	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Result struct {
			Tools []listedTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		t.Fatal(err)
	}

	env := server.NewMCPServer("test", "0.0.0")
	registerEnvironmentTool(env, "", ws.Name, ws.Path, "", ws)
	call := env.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"environment","arguments":{}}}`))
	out, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	orientation := string(out)

	for _, tl := range listing.Result.Tools {
		if tl.Name == "environment" || tl.Name == "tunnel" {
			continue
		}
		if !strings.Contains(orientation, tl.Name) {
			t.Errorf("tool %q is registered but the environment tool does not list it", tl.Name)
		}
	}
}

func mustWriteColdFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
