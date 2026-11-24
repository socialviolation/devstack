package mcp

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

var (
	alice = auth.Caller{Name: "alice", Scope: auth.ScopeAgent}
	owner = auth.Caller{Name: "nick", Scope: auth.ScopeOwner}
)

func guardStore(t *testing.T, recs ...stack.Record) *workspace.Workspace {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := workspace.DataDir("navexa")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"stacks.json", data, 0644); err != nil {
		t.Fatal(err)
	}
	return &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
}

func allToolsServer(t *testing.T, ws *workspace.Workspace) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0", append([]server.ServerOption{server.WithToolCapabilities(true)}, GuardOptions(ws, DefaultAgentLimits)...)...)
	RegisterTools(s, nil, "", nil, ws.Name, ws.Path, ws, nil)
	registerInvestigateTool(s, nil, "", nil, "", ws.Path, ws)
	registerTunnelTool(s, nil, ws)
	return s
}

func TestEveryToolHasAnAgentPolicy(t *testing.T) {
	ws := guardStore(t)
	s := allToolsServer(t, ws)
	for name := range s.ListTools() {
		if _, ok := agentPolicies[name]; !ok {
			t.Errorf("tool %q has no agent policy. Classify it in agentPolicies", name)
		}
	}
	for name := range agentPolicies {
		if s.GetTool(name) == nil {
			t.Errorf("agentPolicies names %q, and no such tool is registered", name)
		}
	}
}

func TestAgentCalls(t *testing.T) {
	ws := guardStore(t,
		stack.Record{Name: "agent-mine", Base: "navexa", Owner: "alice"},
		stack.Record{Name: "agent-theirs", Base: "navexa", Owner: "bob"},
		stack.Record{Name: "agent-legacy", Base: "navexa"},
		stack.Record{Name: "perf", Base: "navexa"},
	)
	g := &guard{ws: ws}

	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"status", nil, ""},
		{"stack_list", nil, ""},
		{"process_logs", map[string]any{"stack": "base"}, "pass stack=agent-<name>"},
		{"process_logs", nil, "pass stack=agent-<name>"},
		{"process_logs", map[string]any{"stack": "agent-mine"}, ""},
		{"investigate", map[string]any{"stack": "agent-mine"}, "may not call investigate"},
		{"start", map[string]any{"stack": "agent-mine"}, ""},
		{"start", map[string]any{"stack": "AGENT-MINE"}, ""},
		{"start", nil, "pass stack=agent-<name>"},
		{"start", map[string]any{"stack": "  "}, "pass stack=agent-<name>"},
		{"restart", map[string]any{"stack": "base"}, "pass stack=agent-<name>"},
		{"stop", map[string]any{"stack": "perf"}, `"perf" is not an agent-* stack`},
		{"stop", map[string]any{"stack": "agent-theirs"}, "belongs to bob"},
		{"stop", map[string]any{"stack": "agent-legacy"}, "belongs to the owner"},
		{"stop", map[string]any{"stack": "agent-gone"}, `stack "agent-gone" does not exist`},
		{"stack_create", map[string]any{"name": "agent-new"}, ""},
		{"stack_create", map[string]any{"name": "agent-theirs"}, "belongs to bob"},
		{"stack_create", map[string]any{"name": "agent-legacy"}, "belongs to the owner"},
		{"stack_create", map[string]any{"name": "feature"}, `"feature" is not an agent-* stack`},
		{"stack_rm", map[string]any{"name": "agent-theirs"}, "belongs to bob"},
		{"stack_rm", nil, "pass name=agent-<name>"},
		{"stack_config", map[string]any{"service": "api"}, "pass stack=agent-<name>"},
		{"stack_config", map[string]any{"service": "api", "stack": "agent-mine"}, ""},
		{"env_which", nil, "pass stack=agent-<name>"},
		{"service_env", map[string]any{"action": "get"}, "pass stack=agent-<name>"},
		{"service_env", map[string]any{"action": "get", "stack": "agent-mine"}, ""},
		{"service_env", map[string]any{"action": "set", "stack": "agent-mine"}, `may not call service_env with action="set"`},
		{"service_env", map[string]any{"action": "bogus", "stack": "agent-mine"}, "may not call service_env"},
		{"hooks", nil, ""},
		{"hooks", map[string]any{"action": "list"}, ""},
		{"hooks", map[string]any{"action": "run", "event": "stack.up"}, "pass stack=agent-<name>"},
		{"hooks", map[string]any{"action": "run", "stack": "agent-mine"}, ""},
		{"task_run", map[string]any{"stack": "agent-mine", "service": "api", "task": "test"}, ""},
		{"task_run", map[string]any{"service": "api", "task": "test"}, "pass stack=agent-<name>"},
		{"task_run", map[string]any{"stack": "agent-theirs", "task": "test"}, "belongs to bob"},
		{"wiring", nil, "pass stack=agent-<name>"},
		{"wiring", map[string]any{"stack": "agent-mine"}, ""},
		{"wiring", map[string]any{"stack": "perf"}, `"perf" is not an agent-* stack`},
		{"observability", map[string]any{"action": "status"}, ""},
		{"observability", map[string]any{"action": "config_on"}, "may not call observability"},
		{"base", map[string]any{"action": "path"}, ""},
		{"base", map[string]any{"action": "sync"}, "may not call base"},
		{"migrate", map[string]any{"action": "list"}, "may not call migrate"},
		{"configure", map[string]any{"stack": "agent-mine"}, "may not call configure"},
		{"tunnel", nil, "may not call tunnel"},
		{"env_set", nil, "may not call env_set"},
		{"env_use", map[string]any{"stack": "agent-mine"}, "may not call env_use"},
		{"doctor", nil, ""},
		{"doctor", map[string]any{"stack": "  "}, ""},
		{"doctor", map[string]any{"stack": "agent-mine"}, ""},
		{"doctor", map[string]any{"stack": "agent-theirs"}, "belongs to bob"},
		{"doctor", map[string]any{"stack": "base"}, "pass stack=agent-<name>"},
		{"doctor", map[string]any{"stack": "perf"}, `"perf" is not an agent-* stack`},
		{"cleanup", map[string]any{"dry_run": true}, "may not call cleanup"},
		{"no_such_tool", nil, "may not call no_such_tool"},
	}
	for _, c := range cases {
		req := mcp.CallToolRequest{}
		req.Params.Name = c.tool
		req.Params.Arguments = c.args
		got := g.check(alice, req)
		if c.want == "" && got != "" {
			t.Errorf("%s %v: denied (%s), want allowed", c.tool, c.args, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s %v: got %q, want a denial containing %q", c.tool, c.args, got, c.want)
		}
	}
}

func callAs(t *testing.T, s *server.MCPServer, c auth.Caller, tool, args string) string {
	t.Helper()
	resp := s.HandleMessage(auth.WithCaller(context.Background(), c), json.RawMessage(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+args+`}}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestGuardDeniesAgentAndPassesOwner(t *testing.T) {
	ws := guardStore(t)
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws, DefaultAgentLimits)...)
	ran := 0
	s.AddTool(mcp.NewTool("restart", mcp.WithString("stack")), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ran++
		return mcp.NewToolResultText("ran"), nil
	})

	out := callAs(t, s, alice, "restart", `{}`)
	if !strings.Contains(out, "agents may only act on their own agent-* stacks") || !strings.Contains(out, `"isError":true`) {
		t.Errorf("agent restart with no stack: want a tool error, got %s", out)
	}
	if ran != 0 {
		t.Fatalf("the handler ran for a denied call")
	}

	out = callAs(t, s, owner, "restart", `{}`)
	if !strings.Contains(out, "ran") || ran != 1 {
		t.Errorf("owner restart: want the handler to run, got %s", out)
	}

	resp := s.HandleMessage(context.Background(), json.RawMessage(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"restart","arguments":{}}}`))
	if data, _ := json.Marshal(resp); !strings.Contains(string(data), "ran") || ran != 2 {
		t.Errorf("stdio caller is the local owner and must pass; got %s", data)
	}
}

func listToolNames(t *testing.T, s *server.MCPServer, c auth.Caller) []string {
	t.Helper()
	resp := s.HandleMessage(auth.WithCaller(context.Background(), c), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range envelope.Result.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func TestAgentToolsListHidesDeniedTools(t *testing.T) {
	ws := guardStore(t)
	s := allToolsServer(t, ws)

	agentSees := strings.Join(listToolNames(t, s, alice), ",")
	for _, hidden := range []string{"configure", "tunnel", "env_set", "env_use", "migrate", "cleanup"} {
		if strings.Contains(","+agentSees+",", ","+hidden+",") {
			t.Errorf("agent tools/list shows %q: %s", hidden, agentSees)
		}
	}
	for _, shown := range []string{"status", "start", "stack_create", "service_env", "base", "observability", "hooks", "doctor"} {
		if !strings.Contains(","+agentSees+",", ","+shown+",") {
			t.Errorf("agent tools/list hides %q: %s", shown, agentSees)
		}
	}

	if got, want := len(listToolNames(t, s, owner)), len(s.ListTools()); got != want {
		t.Errorf("owner tools/list shows %d tools, want all %d", got, want)
	}
}

func TestEnvironmentListsOnlyToolsTheAgentMaySee(t *testing.T) {
	ws := guardStore(t)
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws, DefaultAgentLimits)...)
	registerEnvironmentTool(s, "http://localhost:5080", ws.Name, ws.Path, "", ws)

	toolsLine := func(c auth.Caller) string {
		out := callAs(t, s, c, "environment", `{}`)
		i := strings.Index(out, "tools: ")
		if i < 0 {
			t.Fatalf("environment output has no tools line: %s", out)
		}
		line := out[i:]
		return "," + strings.ReplaceAll(line[len("tools: "):strings.Index(line, `\n`)], " ", "") + ","
	}

	agent := toolsLine(alice)
	for _, hidden := range []string{"configure", "env_set", "env_use", "migrate"} {
		if strings.Contains(agent, ","+hidden+",") {
			t.Errorf("agent environment lists %q: %s", hidden, agent)
		}
	}
	for _, shown := range []string{"status", "start", "process_logs", "stack_create"} {
		if !strings.Contains(agent, ","+shown+",") {
			t.Errorf("agent environment omits %q: %s", shown, agent)
		}
	}
	if own := toolsLine(owner); !strings.Contains(own, ",configure,") || !strings.Contains(own, ",env_set,") {
		t.Errorf("owner environment omits owner tools: %s", own)
	}
}
