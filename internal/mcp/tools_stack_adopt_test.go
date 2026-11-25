package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/stack"
)

func TestStackAdoptDeniesAnAgent(t *testing.T) {
	ws := guardStore(t, stack.Record{Name: "agent-mine", Base: "navexa", Owner: "alice", ExpiresAt: time.Now().Add(time.Hour)})
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws, DefaultAgentLimits)...)
	registerStackAdoptTool(s, ws)

	out := callAs(t, s, alice, "stack_adopt", `{"name":"agent-mine"}`)
	if !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "may not call stack_adopt") {
		t.Errorf("agent stack_adopt: want a denial, got %s", out)
	}
	rec, err := stack.FindStack("navexa", "agent-mine")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Owner != "alice" {
		t.Errorf("denied adopt changed the owner to %q", rec.Owner)
	}
}

func TestStackAdoptOwnerStackIsANoOp(t *testing.T) {
	ws := guardStore(t, stack.Record{Name: "perf", Base: "navexa"})
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws, DefaultAgentLimits)...)
	registerStackAdoptTool(s, ws)

	out := callAs(t, s, owner, "stack_adopt", `{"name":"perf"}`)
	if strings.Contains(out, `"isError":true`) || !strings.Contains(out, "already belongs to the owner") {
		t.Errorf("owner stack_adopt of an owner stack: want a no-op, got %s", out)
	}
}

func TestStackAdoptUnknownNameErrors(t *testing.T) {
	ws := guardStore(t, stack.Record{Name: "agent-a", Base: "navexa", Owner: "alice"})
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws, DefaultAgentLimits)...)
	registerStackAdoptTool(s, ws)

	out := callAs(t, s, owner, "stack_adopt", `{"name":"agent-gone"}`)
	if !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "agent-a (owner alice)") {
		t.Errorf("stack_adopt of an unknown stack: want an error listing agent-a, got %s", out)
	}
}
