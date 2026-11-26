package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func countNotify(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := herdr.Notify
	herdr.Notify = func() { n++ }
	t.Cleanup(func() { herdr.Notify = prev })
	return &n
}

func callStdio(t *testing.T, s *server.MCPServer, tool, args string) string {
	t.Helper()
	resp := s.HandleMessage(context.Background(), json.RawMessage(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+args+`}}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestStackCreateBindsThePaneOverStdioAndNotOverHTTP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERDR_PANE_ID", "w1:p7")
	root := filepath.Join(home, "dev", "navexa")
	gitRepo(t, filepath.Join(root, "api"), "api")
	if err := os.WriteFile(filepath.Join(root, "devstack.workspace.yaml"), []byte("version: 1\nworkspace:\n  name: navexa\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: root}); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatal(err)
	}
	n := countNotify(t)
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerStackCreateTool(s, ws)

	if out := callStdio(t, s, "stack_create", `{"name":"local","repos":"api"}`); strings.Contains(out, `"isError":true`) {
		t.Fatalf("stdio create: %s", out)
	}
	if out := callAs(t, s, owner, "stack_create", `{"name":"remote","repos":"api"}`); strings.Contains(out, `"isError":true`) {
		t.Fatalf("HTTP create: %s", out)
	}

	for name, want := range map[string]string{"local": "w1:p7", "remote": ""} {
		rec, err := stack.FindStack("navexa", name)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Pane != want {
			t.Errorf("%s: pane %q, want %q", name, rec.Pane, want)
		}
	}
	if *n != 2 {
		t.Errorf("publish ran %d times for two creates, want 2", *n)
	}
}

func TestStackAdoptBindsThePaneAndPublishes(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p3")
	ws := guardStore(t, stack.Record{Name: "agent-x", Base: "navexa", Owner: "alice", StopAt: time.Now().Add(time.Hour)})
	n := countNotify(t)
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerStackAdoptTool(s, ws)

	if out := callStdio(t, s, "stack_adopt", `{"name":"agent-x"}`); strings.Contains(out, `"isError":true`) {
		t.Fatalf("adopt: %s", out)
	}
	rec, err := stack.FindStack("navexa", "agent-x")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Pane != "w1:p3" {
		t.Errorf("pane %q, want w1:p3", rec.Pane)
	}
	if *n != 1 {
		t.Errorf("publish ran %d times, want 1", *n)
	}
}

func TestBindPaneIgnoresHTTPCallers(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:server")
	ws := guardStore(t, stack.Record{Name: "perf", Base: "navexa"})
	bindPane(auth.WithCaller(context.Background(), owner), ws, "perf")
	if rec, _ := stack.FindStack("navexa", "perf"); rec.Pane != "" {
		t.Errorf("an HTTP caller bound pane %q", rec.Pane)
	}
	bindPane(context.Background(), ws, "perf")
	if rec, _ := stack.FindStack("navexa", "perf"); rec.Pane != "w1:server" {
		t.Errorf("a stdio caller bound pane %q, want w1:server", rec.Pane)
	}
}

func TestStackDownRemoveAndResyncPublish(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registerStore(t, "navexa",
		stack.Record{Name: "a", Base: "navexa", Active: true, StopAt: time.Now().Add(time.Hour)},
		stack.Record{Name: "b", Base: "navexa"},
		stack.Record{Name: "c", Base: "navexa"},
	)
	ws, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatal(err)
	}
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerStackDownTool(s, ws)
	registerStackRemoveTool(s, ws)
	registerStackResyncTool(s, ws)

	for _, c := range []struct{ tool, args string }{
		{"stack_down", `{"name":"a"}`},
		{"stack_rm", `{"name":"b"}`},
		{"stack_resync", `{"name":"c"}`},
	} {
		n := countNotify(t)
		out := callStdio(t, s, c.tool, c.args)
		if *n != 1 {
			t.Errorf("%s: publish ran %d times, want 1: %s", c.tool, *n, out)
		}
	}
}

func TestSweepPublishesOnlyWhenItStopsAStack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	fixStackClock(t, now)
	registerStore(t, "navexa", stack.Record{Name: "late", Base: "navexa", Active: true, StopAt: now.Add(-time.Minute)})

	n := countNotify(t)
	var out bytes.Buffer
	if _, err := Sweep(true, &out); err != nil {
		t.Fatal(err)
	}
	if *n != 0 {
		t.Errorf("dry run published %d times, want 0", *n)
	}
	if _, err := Sweep(false, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if *n != 1 {
		t.Errorf("sweep published %d times, want 1", *n)
	}
	if _, err := Sweep(false, &out); err != nil {
		t.Fatal(err)
	}
	if *n != 1 {
		t.Errorf("a sweep that stopped nothing published: %d times, want 1", *n)
	}
}
