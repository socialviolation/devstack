package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/workspace"
)

// registeredToolNames lists what RegisterTools actually exposes.
func registeredToolNames(t *testing.T) map[string]bool {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	RegisterTools(s, nil, "", nil, ws.Name, ws.Path, ws, nil)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range out.Result.Tools {
		names[tool.Name] = true
	}
	return names
}

// The session briefing tells an agent that the tools mirror the commands, and
// names the operations that have no tool. Both halves are claims that go stale
// the moment someone adds a tool, and a briefing that lies about what exists
// sends an agent to the shell for something it could have called, or to a tool
// that is not there.
func TestBriefingParityClaimHolds(t *testing.T) {
	names := registeredToolNames(t)

	// Named in the briefing as tools that exist.
	for _, want := range []string{"status", "start", "stop", "restart", "stack_up", "env_use", "environment", "migrate"} {
		if !names[want] {
			t.Errorf("the briefing names %q as a tool, and it is not registered", want)
		}
	}

	// Named in the briefing as shell-only. A tool covering one of these means
	// the briefing is sending agents to the shell needlessly.
	shellOnly := map[string]string{
		"workspace_up":   "workspace up and down",
		"workspace_down": "workspace up and down",
		"ports":          "ports",
		"ports_free":     "ports",
		"dependencies":   "dependencies",
		"deps":           "dependencies",
		"group_add":      "group add and remove",
		"groups":         "group add and remove",
		"stack_config":   "stack config",
		"init":           "init",
	}
	for tool, claim := range shellOnly {
		if names[tool] {
			t.Errorf("the briefing says %q needs the shell, but tool %q is now registered — update writePrimeWhatThisIs", claim, tool)
		}
	}
}

// The briefing tells agents to call `environment` first, so it has to exist
// unconditionally rather than being gated on workspace configuration.
func TestEnvironmentToolIsAlwaysRegistered(t *testing.T) {
	if !registeredToolNames(t)["environment"] {
		t.Fatal("environment is the tool the briefing sends every agent to first")
	}
}

func TestParityClaimNamesNoToolThatVanished(t *testing.T) {
	names := registeredToolNames(t)
	var missing []string
	for _, n := range []string{"hooks", "tunnel", "topology", "service_env", "observability", "process_logs"} {
		if !names[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("tools disappeared from RegisterTools: %s", strings.Join(missing, ", "))
	}
}
