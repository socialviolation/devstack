package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

const wiringAPI = `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
ports:
  http: 5001
`

func wiringWeb(path string) string {
	return `version: 1
service:
  name: web
runtime:
  run:
    command: npm start
ports:
  http: 5000
env:
  values:
    API_URL: ${api.url}/` + path + `
`
}

func seedWiringWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	writeFile(t, filepath.Join(root, "api", config.ServiceManifestFileName), wiringAPI)
	writeFile(t, filepath.Join(root, "web", config.ServiceManifestFileName), wiringWeb("v1-base"))
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos: [./api, ./web]
dependencies:
  web: [api]
`)

	stackRoot := filepath.Join(home, "stacks", "agent-mine")
	worktree := filepath.Join(home, "wt", "web")
	writeFile(t, filepath.Join(stackRoot, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa--agent-mine
  repoDiscovery:
    mode: explicit
    repos:
      - `+worktree+`
`)
	writeFile(t, filepath.Join(worktree, config.ServiceManifestFileName), wiringWeb("v1-stack"))

	data, err := json.Marshal([]stack.Record{{
		Name:      "agent-mine",
		Base:      "navexa",
		Root:      stackRoot,
		Owner:     "alice",
		Members:   []string{"web"},
		Worktrees: map[string]string{"web": worktree},
	}})
	if err != nil {
		t.Fatal(err)
	}
	storeDir := workspace.DataDir("navexa")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "stacks.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	ws := &workspace.Workspace{Name: "navexa", Path: root}
	if err := workspace.Register(*ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func wiringServer(t *testing.T, ws *workspace.Workspace) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerWiringTool(s, ws, ws.Path)
	return s
}

func TestWiringShowsBaseAndStackCopies(t *testing.T) {
	ws := seedWiringWorkspace(t)
	out := callAs(t, wiringServer(t, ws), owner, "wiring", `{"stack":"agent-mine"}`)
	for _, want := range []string{
		"web → api",
		"API_URL  via ${api.url}",
		"base: http://localhost:5001/v1-base → base api",
		"agent-mine: http://localhost:5001/v1-stack → base api",
		"! stack agent-mine web → base api",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("owner output omits %q:\n%s", want, out)
		}
	}
}

func TestWiringHidesBaseValuesFromAgents(t *testing.T) {
	ws := seedWiringWorkspace(t)
	s := wiringServer(t, ws)

	out := callAs(t, s, alice, "wiring", `{"stack":"agent-mine"}`)
	if strings.Contains(out, "v1-base") {
		t.Errorf("agent output shows a base value:\n%s", out)
	}
	if !strings.Contains(out, "base: → base api") || !strings.Contains(out, "v1-stack") {
		t.Errorf("agent output must show the edge and its own stack's value:\n%s", out)
	}

	for _, args := range []string{`{}`, `{"stack":"base"}`} {
		out := callAs(t, s, alice, "wiring", args)
		if !strings.Contains(out, "agents may only act on their own agent-* stacks") || strings.Contains(out, "v1-base") {
			t.Errorf("agent wiring %s: want a refusal:\n%s", args, out)
		}
	}
}

func TestWiringRefusesStackBase(t *testing.T) {
	ws := seedWiringWorkspace(t)
	out := callAs(t, wiringServer(t, ws), owner, "wiring", `{"stack":"base"}`)
	if !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "is not in workspace") {
		t.Errorf("stack base names no stack, and wiring must refuse it:\n%s", out)
	}
}

func TestWiringNamesTheEdgeKindAndVia(t *testing.T) {
	ws := seedWiringWorkspace(t)
	if out := callAs(t, wiringServer(t, ws), owner, "wiring", `{}`); !strings.Contains(out, "web → api  (calls)") {
		t.Errorf("a legacy dependency must show as calls:\n%s", out)
	}

	writeFile(t, filepath.Join(ws.Path, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos: [./api, ./web]
`)
	writeFile(t, filepath.Join(ws.Path, "web", config.ServiceManifestFileName), wiringWeb("v1-base")+`needs:
  - service: api
    via: http
`)
	if out := callAs(t, wiringServer(t, ws), owner, "wiring", `{}`); !strings.Contains(out, "web → api  (needs, via http)") {
		t.Errorf("a needs edge must show its kind and via:\n%s", out)
	}
}
