package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func noteServer(t *testing.T, rec stack.Record) *server.MCPServer {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	dir := workspace.DataDir("navexa")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]stack.Record{rec})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"stacks.json", data, 0644); err != nil {
		t.Fatal(err)
	}

	s := server.NewMCPServer("test", "0.0.0")
	registerStackNoteTool(s, &workspace.Workspace{Name: "navexa"})
	return s
}

func callNote(t *testing.T, s *server.MCPServer, args string) string {
	t.Helper()
	resp := s.HandleMessage(context.Background(), json.RawMessage(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stack_note","arguments":`+args+`}}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestStackNoteAppendAddsAnEntryAndKeepsThePurpose(t *testing.T) {
	s := noteServer(t, stack.Record{Name: "perf", Base: "navexa", Note: "NAV-412 daily value spike"})

	out := callNote(t, s, `{"name":"perf","append":"parked: needs a prod diff before merge"}`)
	if !strings.Contains(out, "parked: needs a prod diff before merge") {
		t.Fatalf("append must confirm the entry; got %s", out)
	}

	rec, err := stack.FindStack("navexa", "perf")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Note != "NAV-412 daily value spike" {
		t.Fatalf("Note = %q, want the purpose untouched by an append", rec.Note)
	}
	if len(rec.Log) != 1 {
		t.Fatalf("Log = %v, want the one appended entry", rec.Log)
	}
}

// The purpose and the progress are different fields with different lifetimes.
// Accepting both in one call would make it ambiguous which the caller meant to
// keep, so the tool refuses instead of guessing.
func TestStackNoteRefusesNoteAndAppendTogether(t *testing.T) {
	s := noteServer(t, stack.Record{Name: "perf", Base: "navexa"})

	out := callNote(t, s, `{"name":"perf","note":"a purpose","append":"an entry"}`)
	if !strings.Contains(out, "not both") {
		t.Fatalf("passing note and append together must be refused; got %s", out)
	}

	rec, err := stack.FindStack("navexa", "perf")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Note != "" || len(rec.Log) != 0 {
		t.Fatalf("Note = %q, Log = %v, want a refused call to write nothing", rec.Note, rec.Log)
	}
}

func TestStackNoteReadReturnsPurposeAndEntries(t *testing.T) {
	s := noteServer(t, stack.Record{Name: "perf", Base: "navexa", Note: "NAV-412 daily value spike"})

	callNote(t, s, `{"name":"perf","append":"repro on dev2"}`)
	callNote(t, s, `{"name":"perf","append":"FX join fixed, spike halved"}`)

	out := callNote(t, s, `{"name":"perf"}`)
	for _, want := range []string{"NAV-412 daily value spike", "repro on dev2", "FX join fixed, spike halved"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read must return the purpose and every entry; %q missing from %s", want, out)
		}
	}
}

func TestStackNoteRepeatedEntryIsRejectedNotStacked(t *testing.T) {
	s := noteServer(t, stack.Record{Name: "perf", Base: "navexa"})

	callNote(t, s, `{"name":"perf","append":"blocked on ccxt rate limit"}`)
	out := callNote(t, s, `{"name":"perf","append":"blocked on ccxt rate limit"}`)
	if !strings.Contains(out, "Not appended") {
		t.Fatalf("a repeat of the last entry must be reported as a no-op; got %s", out)
	}

	rec, err := stack.FindStack("navexa", "perf")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Log) != 1 {
		t.Fatalf("Log = %v, want the repeat dropped", rec.Log)
	}
}

// The caps stop a log from being flooded, but only the description stops the
// flooding from being attempted. It has to say when NOT to write, because a
// model with a writable field and no instruction not to use it will use it.
func TestStackNoteDescriptionDiscouragesNarration(t *testing.T) {
	s := noteServer(t, stack.Record{Name: "perf", Base: "navexa"})

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	listing := string(data)

	for _, want := range []string{
		"Do NOT append",
		"per file edited",
		"If in doubt, do not append",
		"last 5 entries are kept",
	} {
		if !strings.Contains(listing, want) {
			t.Errorf("append parameter must state %q, or nothing bounds how often it is written; got %s", want, listing)
		}
	}
}
