package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/observability"
	"github.com/socialviolation/devstack/internal/workspace"
)

func TestResolveInvestigateStack(t *testing.T) {
	cases := map[string]string{
		"":     "base",
		"  ":   "base",
		"all":  "",
		"ALL":  "",
		"*":    "",
		"perf": "perf",
		"base": "base",
	}
	for in, want := range cases {
		if got := observability.ResolveStackFilter(in); got != want {
			t.Errorf("ResolveStackFilter(%q) = %q, want %q", in, got, want)
		}
	}
}

// Service control is no longer gated on an environment type: RegisterTools must
// register the full local tool set for any workspace.
func TestRegisterToolsAlwaysRegistersServiceControl(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}

	RegisterTools(s, nil, "", nil, ws.Name, ws.Path, ws)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	listing := string(data)
	want := []string{
		"environment", "status", "restart", "stop", "configure", "process_logs",
		"service_env", "observability",
		"stack_create", "stack_list", "stack_up", "stack_down", "stack_rm",
		"env_use", "env_which", "env_set",
	}
	for _, name := range want {
		if !strings.Contains(listing, `"`+name+`"`) {
			t.Errorf("tools/list missing %q; got %s", name, listing)
		}
	}
}

func TestStackAndEnvToolsRegistered(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	registerStackTools(s, ws)
	registerEnvTools(s, ws, ws.Path)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	listing := string(data)
	for _, name := range []string{"stack_up", "stack_down", "stack_create", "stack_list", "stack_rm", "env_use", "env_which", "env_set"} {
		if !strings.Contains(listing, `"`+name+`"`) {
			t.Errorf("tools/list missing %q; got %s", name, listing)
		}
	}
}

// investigate and `devstack otel traces` are the same capability with the same
// word, and they used to default opposite ways: a human and an agent comparing
// notes on an unqualified query disagreed about what it had covered. Both
// surfaces now quote one sentence, so neither can state a different default.
func TestInvestigateStatesTheSharedStackDefault(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	registerInvestigateTool(s, nil, "", nil, "", ws.Path, ws)

	desc := listTools(t, s)["investigate"].InputSchema.Properties["stack"].Description
	if !strings.Contains(desc, observability.StackScopeDesc) {
		t.Errorf("investigate's stack parameter must state the shared default %q; got %q", observability.StackScopeDesc, desc)
	}
	if !strings.Contains(observability.StackScopeDesc, "base only") || !strings.Contains(observability.StackScopeDesc, "\"all\"") {
		t.Errorf("the shared sentence must state the default and how to widen it: %q", observability.StackScopeDesc)
	}
}
