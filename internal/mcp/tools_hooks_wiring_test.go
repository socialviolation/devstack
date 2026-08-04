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
	"github.com/socialviolation/devstack/internal/workspace"
)

// mcpFiredEvents are the events the MCP tools must fire. The MCP tools reach
// stack.Create/Remove/SetActive and the daemon directly rather than through cmd,
// so a firing point added to the CLI alone leaves an agent driving a stack with
// no automation at all — which is the case hooks exist for.
//
// workspace.up / workspace.down are absent deliberately: there is no MCP tool
// for them (AGENTS.md sends you to the shell), so nothing here can fire them.
var mcpFiredEvents = map[string]string{
	config.EventStackCreate:  "config.EventStackCreate",
	config.EventStackUp:      "config.EventStackUp",
	config.EventStackDown:    "config.EventStackDown",
	config.EventStackDestroy: "config.EventStackDestroy",
	config.EventServiceStart: "config.EventServiceStart",
	config.EventServiceStop:  "config.EventServiceStop",
}

func TestMCPLifecycleToolsFireHooks(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read mcp dir: %v", err)
	}

	var sources strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "tools_hooks.go" {
			continue // the hooks tool resolves and re-runs events, it does not own a firing point
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sources.Write(data)
	}
	body := sources.String()

	for event, constant := range mcpFiredEvents {
		if !strings.Contains(body, constant) {
			t.Errorf("no MCP tool fires %q (%s) — an agent driving this lifecycle step would run no hooks, while the CLI would", event, constant)
		}
	}
	if !strings.Contains(body, "hooks.Fire(") {
		t.Error("no MCP tool calls hooks.Fire")
	}
}

// Every event the CLI fires and MCP can also perform must be fired by both, or
// the same operation behaves differently depending on which surface reached it.
func TestMCPFiresEveryEventItCanPerform(t *testing.T) {
	for _, event := range config.HookEvents() {
		switch event {
		case config.EventWorkspaceUp, config.EventWorkspaceDown:
			if _, ok := mcpFiredEvents[event]; ok {
				t.Errorf("%s is listed as MCP-fired but there is no MCP tool for it", event)
			}
		default:
			if _, ok := mcpFiredEvents[event]; !ok {
				t.Errorf("event %q has no MCP firing point declared — if a new MCP tool performs it, wire hooks.Fire and list it here", event)
			}
		}
	}
}

func TestHooksToolIsRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}

	RegisterTools(s, nil, "", nil, ws.Name, ws.Path, ws, nil)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"hooks"`) {
		t.Errorf("tools/list missing %q; got %s", "hooks", string(data))
	}
}
