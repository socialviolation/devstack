package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

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
		if got := resolveInvestigateStack(in); got != want {
			t.Errorf("resolveInvestigateStack(%q) = %q, want %q", in, got, want)
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
