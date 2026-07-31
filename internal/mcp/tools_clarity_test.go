package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	_ "github.com/socialviolation/devstack/internal/otel/plugins/openobserve" // the environment tool names the active backend plugin
	"github.com/socialviolation/devstack/internal/workspace"
)

// clarityWorkspace lays down a workspace whose manifest declares a group, with
// observability enabled so the environment tool reports the investigate tools.
func clarityWorkspace(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
groups:
  core:
    - api
observability:
  enabled: true
`)
	writeFile(t, filepath.Join(root, "repos", "api", config.ServiceManifestFileName), basicService)
	return &workspace.Workspace{Name: "testws", Path: root}, root
}

// clarityCallTool invokes a registered tool and returns its text result.
func clarityCallTool(t *testing.T, s *server.MCPServer, name string) string {
	t.Helper()
	resp := s.HandleMessage(context.Background(), json.RawMessage(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":{}}}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// clarityToolListing returns the JSON tools/list payload for the given server.
func clarityToolListing(t *testing.T, s *server.MCPServer) string {
	t.Helper()
	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Five tools take a group parameter and none of them lists the groups, so the
// orientation tool has to.
func TestClarityEnvironmentListsGroups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, root := clarityWorkspace(t)

	s := server.NewMCPServer("test", "0.0.0")
	registerEnvironmentTool(s, "http://localhost:5080", ws.Name, root, "", ws)

	out := clarityCallTool(t, s, "environment")
	if !strings.Contains(out, "groups: core") {
		t.Errorf("environment must name the workspace's groups; got %s", out)
	}
}

func TestClarityEnvironmentOmitsGroupsLineWhenThereAreNone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, root, _ := newTestWorkspace(t, true, basicService)

	s := server.NewMCPServer("test", "0.0.0")
	registerEnvironmentTool(s, "http://localhost:5080", ws.Name, root, "", ws)

	out := clarityCallTool(t, s, "environment")
	if strings.Contains(out, "groups:") {
		t.Errorf("a workspace with no groups must not print a groups line; got %s", out)
	}
}

// The scope line must not claim a default service for every investigate mode:
// the attribute search has none and a trace lookup ignores service and stack.
func TestClarityEnvironmentScopeMatchesInvestigate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, root := clarityWorkspace(t)

	s := server.NewMCPServer("test", "0.0.0")
	registerEnvironmentTool(s, "http://localhost:5080", ws.Name, root, "", ws)

	out := clarityCallTool(t, s, "environment")
	for _, want := range []string{
		"query scope:",
		"attribute search has no default service",
		"trace_id/span_id lookup ignores service and stack",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scope line must state %q; got %s", want, out)
		}
	}
	if strings.Contains(out, "an unqualified call narrows to the service this server runs in") {
		t.Errorf("scope line must not generalise the service default to every mode; got %s", out)
	}
}

// An agent that finds a stack listed as inactive must learn from the same output
// what querying it does.
func TestClarityStacksSummaryDefinesInactive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeStacksStore(t, "navexa", `[
	  {"name":"perf","base":"navexa","root":"/x/perf","branch":"perf",
	   "overlay":["navexa-api"],"worktrees":{"navexa-api":"/x/perf/navexa-api"},
	   "ports":{"navexa-api/http":20000},"daemon_port":10351,"created_at":"2026-07-21T00:00:00Z"}
	]`)

	got := stacksSummary(&workspace.Workspace{Name: "navexa"})
	for _, want := range []string{
		"inactive =",
		"error \"not up\" instead of falling through to base",
		"service_env still reads and writes its worktree config",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("an inactive stack must be defined at the point it is listed: missing %q in %q", want, got)
		}
	}
}

func TestClarityStacksSummaryOmitsInactiveNoteWhenAllActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeStacksStore(t, "navexa", `[
	  {"name":"perf","base":"navexa","root":"/x/perf","branch":"perf","active":true,
	   "overlay":["navexa-api"],"worktrees":{"navexa-api":"/x/perf/navexa-api"},
	   "ports":{"navexa-api/http":20000},"daemon_port":10351,"created_at":"2026-07-21T00:00:00Z"}
	]`)

	got := stacksSummary(&workspace.Workspace{Name: "navexa"})
	if !strings.Contains(got, "perf (active") {
		t.Fatalf("fixture must produce an active stack; got %q", got)
	}
	if strings.Contains(got, "inactive =") {
		t.Errorf("no stack is inactive, so the definition must not be printed; got %q", got)
	}
}

// Tool annotations are per-tool while these actions differ, so the action
// parameter has to say which actions only read.
func TestClarityActionsDocumentReadsAndWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, root := clarityWorkspace(t)

	s := server.NewMCPServer("test", "0.0.0")
	registerObservabilityTool(s, ws, root)
	registerServiceEnvTool(s, ws, root)

	listing := clarityToolListing(t, s)
	for _, want := range []string{
		"status, variants — read only, change nothing",
		"get, diff, check, drift — read only, change nothing",
		"set — writes a file",
	} {
		if !strings.Contains(listing, want) {
			t.Errorf("action description must state %q; got %s", want, listing)
		}
	}
}

// service_env and env_which both answer "what config does this service run
// with", so service_env must say when to reach for the other one.
func TestClarityServiceEnvPointsAtEnvWhich(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, root := clarityWorkspace(t)

	s := server.NewMCPServer("test", "0.0.0")
	registerServiceEnvTool(s, ws, root)

	listing := clarityToolListing(t, s)
	if !strings.Contains(listing, "reach for env_which instead") {
		t.Errorf("service_env must draw the boundary against env_which; got %s", listing)
	}
}

// "generate" is not a tool an agent can call, so the write confirmation must name
// what actually applies the write.
func TestClaritySetNamesRestartNotGenerate(t *testing.T) {
	ws, root, _ := newTestWorkspace(t, false, basicService)

	res, err := handleServiceEnvSet(ws, root, "", "api", "API_URL", "http://localhost:8080", "manifest")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	out := resultText(t, res)

	if strings.Contains(out, "generate + restart") {
		t.Errorf("the confirmation must not require an undefined generate step; got %q", out)
	}
	for _, want := range []string{"next restart", "devstack service restart api", "no separate generate step"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirmation must state %q; got %q", want, out)
		}
	}
}

// A stack's overlay is the answer to "what is actually running in here", and it
// was recorded from the start but never shown.
func TestClarityStackListShowsOverlayBranchAndNote(t *testing.T) {
	ws, _ := clarityWorkspace(t)
	s := server.NewMCPServer("test", "0.0.0")
	registerStackTools(s, ws)
	listing := clarityToolListing(t, s)

	for _, want := range []string{"overlays", "branch", "note"} {
		if !strings.Contains(strings.ToLower(listing), want) {
			t.Errorf("stack_list should say it reports %q", want)
		}
	}
	if !strings.Contains(listing, "the branch says what changed") {
		t.Error("stack_create's note parameter should say why the note exists")
	}
}
