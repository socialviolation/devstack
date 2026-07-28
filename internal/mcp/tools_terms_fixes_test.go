package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/workspace"
)

type listedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Annotations struct {
		ReadOnlyHint    *bool `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
		IdempotentHint  *bool `json:"idempotentHint"`
		OpenWorldHint   *bool `json:"openWorldHint"`
	} `json:"annotations"`
	InputSchema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	} `json:"inputSchema"`
}

func listEnvStackTools(t *testing.T) map[string]listedTool {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	registerStackTools(s, ws)
	registerEnvTools(s, ws, ws.Path)
	registerServiceEnvTool(s, ws, ws.Path)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result struct {
			Tools []listedTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal tools/list: %v (%s)", err, data)
	}
	out := map[string]listedTool{}
	for _, tool := range envelope.Result.Tools {
		out[tool.Name] = tool
	}
	return out
}

func TestEnvStackToolAnnotationsAreHonest(t *testing.T) {
	tools := listEnvStackTools(t)

	want := map[string]struct{ readOnly, destructive, idempotent, openWorld bool }{
		"env_which":    {readOnly: true, destructive: false, idempotent: true, openWorld: false},
		"stack_list":   {readOnly: true, destructive: false, idempotent: true, openWorld: false},
		"env_use":      {readOnly: false, destructive: false, idempotent: true, openWorld: false},
		"env_set":      {readOnly: false, destructive: false, idempotent: true, openWorld: false},
		"service_env":  {readOnly: false, destructive: false, idempotent: true, openWorld: false},
		"stack_create": {readOnly: false, destructive: false, idempotent: false, openWorld: false},
		"stack_up":     {readOnly: false, destructive: false, idempotent: true, openWorld: false},
		"stack_down":   {readOnly: false, destructive: false, idempotent: true, openWorld: false},
		"stack_rm":     {readOnly: false, destructive: true, idempotent: false, openWorld: false},
	}

	for name, exp := range want {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		a := tool.Annotations
		if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Fatalf("%s: annotations left unset: %+v", name, a)
		}
		if *a.ReadOnlyHint != exp.readOnly {
			t.Errorf("%s: readOnlyHint = %v, want %v", name, *a.ReadOnlyHint, exp.readOnly)
		}
		if *a.DestructiveHint != exp.destructive {
			t.Errorf("%s: destructiveHint = %v, want %v", name, *a.DestructiveHint, exp.destructive)
		}
		if *a.IdempotentHint != exp.idempotent {
			t.Errorf("%s: idempotentHint = %v, want %v", name, *a.IdempotentHint, exp.idempotent)
		}
		if *a.OpenWorldHint != exp.openWorld {
			t.Errorf("%s: openWorldHint = %v, want %v", name, *a.OpenWorldHint, exp.openWorld)
		}
	}
}

func TestServiceEnvDefinesItsTerms(t *testing.T) {
	tool := listEnvStackTools(t)["service_env"]

	for _, want := range []string{"service env.values", "active env", "devstack-computed", "config.sources"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("service_env description does not define %q: %s", want, tool.Description)
		}
	}
	if !strings.Contains(tool.InputSchema.Properties["group"].Description, "workspace manifest") {
		t.Errorf("group parameter does not say where groups are declared: %s", tool.InputSchema.Properties["group"].Description)
	}
	stackDesc := tool.InputSchema.Properties["stack"].Description
	if !strings.Contains(stackDesc, "Source-tree semantics") {
		t.Errorf("service_env stack parameter does not flag its source-tree semantics: %s", stackDesc)
	}
}

func TestStackToolsStateShortNameRuleAndServiceLinks(t *testing.T) {
	tools := listEnvStackTools(t)

	if !strings.Contains(tools["stack_list"].Description, "<base>--<name>") {
		t.Errorf("stack_list does not state the short-name vs identity rule: %s", tools["stack_list"].Description)
	}
	for _, name := range []string{"stack_up", "stack_down", "stack_rm"} {
		if !strings.Contains(tools[name].InputSchema.Properties["name"].Description, "SHORT name") {
			t.Errorf("%s name parameter does not ask for the short name: %s", name, tools[name].InputSchema.Properties["name"].Description)
		}
	}
	for _, name := range []string{"stack_create", "stack_list", "stack_up"} {
		if !strings.Contains(tools[name].Description, "http://localhost:<port>") {
			t.Errorf("%s does not say what a service link is: %s", name, tools[name].Description)
		}
	}
}

func TestEnvSetWarnsItWritesACommittedFile(t *testing.T) {
	tool := listEnvStackTools(t)["env_set"]

	for _, want := range []string{"committed to git", "service_env"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("env_set description missing %q: %s", want, tool.Description)
		}
	}
}
