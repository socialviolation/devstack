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

func TestRenderVariantsNamesStackEnvAndReportedName(t *testing.T) {
	got := renderVariants([]observability.ServiceVariant{
		{Service: "Navexa.API", Devstack: "navexa-api", Stack: "", Env: "local", Spans: 12},
		{Service: "navexa-web", Devstack: "navexa-web", Stack: "feat-x", Spans: 3},
	})

	for _, want := range []string{
		"Navexa.API (devstack: navexa-api) stack=base env=local spans=12",
		"navexa-web stack=feat-x spans=3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderVariants output missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "(devstack: navexa-web)") {
		t.Errorf("renderVariants repeated an identical devstack name; got:\n%s", got)
	}
}

func TestRenderVariantsEmpty(t *testing.T) {
	got := renderVariants(nil)
	if !strings.Contains(got, "No variant reported telemetry") {
		t.Errorf("unexpected empty rendering: %q", got)
	}
}

func TestObservabilityToolAdvertisesVariantsAndHonestAnnotations(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	registerObservabilityTool(s, ws, ws.Path)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	listing := string(data)

	for _, want := range []string{
		`"readOnlyHint":false`,
		`"destructiveHint":false`,
		`"idempotentHint":true`,
		`"openWorldHint":true`,
		"status, variants — read only, change nothing",
	} {
		if !strings.Contains(listing, want) {
			t.Errorf("tools/list missing %q; got %s", want, listing)
		}
	}
}

func TestObservabilityRejectsUnknownActionNamingVariants(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	registerObservabilityTool(s, ws, ws.Path)

	resp := s.HandleMessage(context.Background(), json.RawMessage(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observability","arguments":{"action":"services"}}}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status, variants, enable, disable, or configure") {
		t.Errorf("unknown action did not point at the variants action; got %s", data)
	}
}
