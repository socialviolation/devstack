package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func fixStackClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := stack.Now
	stack.Now = func() time.Time { return now }
	t.Cleanup(func() { stack.Now = prev })
}

func registerStore(t *testing.T, name string, recs ...stack.Record) {
	t.Helper()
	if err := workspace.Register(workspace.Workspace{Name: name, Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	dir := workspace.DataDir(name)
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
}

func gitRepo(t *testing.T, dir, service string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devstack.service.yaml"), []byte("version: 1\nservice:\n  name: "+service+"\nruntime:\n  run:\n    command: go run .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-f", "."},
		{"-c", "user.email=t@example.com", "-c", "user.name=tester", "commit", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestAnAgentCreatesFiveStacksWithNoQuota(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerStackCreateTool(s, ws)

	for i := 1; i <= 5; i++ {
		out := callAs(t, s, alice, "stack_create", fmt.Sprintf(`{"name":"agent-%d","repos":"api"}`, i))
		if strings.Contains(out, `"isError":true`) {
			t.Fatalf("agent stack %d refused: %s", i, out)
		}
	}
	recs, err := stack.LoadStore("navexa")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 5 {
		t.Fatalf("store holds %d stacks, want 5", len(recs))
	}
	for _, r := range recs {
		if r.Owner != "alice" || r.StopAt.IsZero() {
			t.Errorf("%s: owner %q, stop time %v, want alice and a stop time", r.Name, r.Owner, r.StopAt)
		}
	}
}

func TestSweepStopsOnlyUpStacksPastTheirStopTimeInEveryWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	fixStackClock(t, now)
	registerStore(t, "navexa",
		stack.Record{Name: "late", Base: "navexa", Active: true, StopAt: now.Add(-time.Minute)},
		stack.Record{Name: "fresh", Base: "navexa", Active: true, StopAt: now.Add(time.Hour)},
		stack.Record{Name: "down", Base: "navexa", StopAt: now.Add(-time.Hour)},
	)
	registerStore(t, "other",
		stack.Record{Name: "agent-late", Base: "other", Owner: "alice", Active: true, StopAt: now.Add(-time.Hour)},
	)

	var out bytes.Buffer
	n, err := Sweep(false, &out)
	if err != nil {
		t.Fatalf("Sweep: %v\n%s", err, out.String())
	}
	if n != 2 {
		t.Errorf("Sweep found %d stacks, want 2:\n%s", n, out.String())
	}

	for _, c := range []struct {
		ws, name string
		stopped  bool
	}{
		{"navexa", "late", true},
		{"navexa", "fresh", false},
		{"navexa", "down", false},
		{"other", "agent-late", true},
	} {
		rec, err := stack.FindStack(c.ws, c.name)
		if err != nil {
			t.Errorf("%s/%s: the sweep removed the record: %v", c.ws, c.name, err)
			continue
		}
		if !c.stopped {
			if rec.StoppedBy != "" || len(rec.Log) > 0 {
				t.Errorf("%s/%s: the sweep touched it: by=%q log=%v", c.ws, c.name, rec.StoppedBy, rec.Log)
			}
			continue
		}
		if rec.Active || rec.StoppedBy != stack.StoppedBySweep || !rec.StoppedAt.Equal(now) {
			t.Errorf("%s/%s: active=%v by=%q at=%v, want stopped by the sweep at %v", c.ws, c.name, rec.Active, rec.StoppedBy, rec.StoppedAt, now)
		}
		if last, ok := rec.LatestEntry(); !ok || last.Text != "stopped by devstack: 24h since up" {
			t.Errorf("%s/%s: log = %v, want the sweep entry", c.ws, c.name, rec.Log)
		}
	}
	if fresh, _ := stack.FindStack("navexa", "fresh"); !fresh.Active {
		t.Error("the sweep stopped a stack before its stop time")
	}
}

func TestSweepDryRunChangesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	fixStackClock(t, now)
	registerStore(t, "navexa",
		stack.Record{Name: "late", Base: "navexa", Active: true, StopAt: now.Add(-time.Minute)},
		stack.Record{Name: "fresh", Base: "navexa", Active: true, StopAt: now.Add(time.Hour)},
	)
	before, err := os.ReadFile(workspace.DataDir("navexa") + "stacks.json")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := Sweep(true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would stop navexa--late") || strings.Contains(out.String(), "fresh") {
		t.Errorf("dry run output = %q, want late alone", out.String())
	}
	after, err := os.ReadFile(workspace.DataDir("navexa") + "stacks.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("dry run changed the store:\nbefore %s\nafter  %s", before, after)
	}
}

func TestSweepDoesNotStopAStackExtendedAfterTheScan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	fixStackClock(t, now)
	registerStore(t, "navexa",
		stack.Record{Name: "extended", Base: "navexa", Active: true, StopAt: now.Add(time.Hour)},
		stack.Record{Name: "down", Base: "navexa", StopAt: now.Add(-time.Hour)},
	)
	ws, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"extended", "down"} {
		if res, stopped := stackDown(ws, name, stack.StoppedBySweep, now); stopped {
			t.Errorf("%s: the sweep stopped it: %s", name, toolResultText(res))
		}
		rec, err := stack.FindStack("navexa", name)
		if err != nil {
			t.Fatal(err)
		}
		if rec.StoppedBy != "" || !rec.StoppedAt.IsZero() {
			t.Errorf("%s: by=%q at=%v, want untouched", name, rec.StoppedBy, rec.StoppedAt)
		}
	}
	if rec, _ := stack.FindStack("navexa", "extended"); !rec.Active {
		t.Error("the sweep marked the extended stack down")
	}
}

func TestMarkDownIfDueSkipsAStackThatIsNotDue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	fixStackClock(t, now)
	registerStore(t, "navexa",
		stack.Record{Name: "late", Base: "navexa", Active: true, StopAt: now.Add(-time.Minute)},
		stack.Record{Name: "fresh", Base: "navexa", Active: true, StopAt: now.Add(time.Hour)},
	)
	for _, c := range []struct {
		name string
		due  bool
	}{{"late", true}, {"fresh", false}} {
		due, err := stack.MarkDownIfDue("navexa", c.name, stack.StoppedBySweep, now)
		if err != nil || due != c.due {
			t.Errorf("%s: MarkDownIfDue = %v, %v; want %v", c.name, due, err, c.due)
		}
		if rec, _ := stack.FindStack("navexa", c.name); rec.Active == c.due {
			t.Errorf("%s: active=%v after MarkDownIfDue", c.name, rec.Active)
		}
	}
}

func TestStackListShowsOwnerAndStopTime(t *testing.T) {
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.Local)
	fixStackClock(t, now)
	ws := guardStore(t,
		stack.Record{Name: "agent-a", Base: "navexa", Owner: "alice", Active: true, StopAt: now.Add(5 * time.Hour)},
		stack.Record{Name: "perf", Base: "navexa", StoppedBy: stack.StoppedBySweep, StoppedAt: now.Add(-time.Hour)},
	)
	s := server.NewMCPServer("test", "0.0.0")
	registerStackListTool(s, ws)

	out := callAs(t, s, owner, "stack_list", `{}`)
	for _, want := range []string{"owner:    alice", "up — stops 14:00 today", "stopped by devstack 2026-11-26 08:00"} {
		if !strings.Contains(out, want) {
			t.Errorf("stack_list lacks %q: %s", want, out)
		}
	}
	if strings.Count(out, "owner:") != 1 {
		t.Errorf("stack_list shows an owner for a stack that has none: %s", out)
	}
}

func TestStackExtendIsOwnStackOnly(t *testing.T) {
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	fixStackClock(t, now)
	ws := guardStore(t,
		stack.Record{Name: "agent-a", Base: "navexa", Owner: "alice", StopAt: now.Add(time.Hour)},
		stack.Record{Name: "agent-b", Base: "navexa", Owner: "bob", StopAt: now.Add(time.Hour)},
	)
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerStackExtendTool(s, ws)

	if out := callAs(t, s, alice, "stack_extend", `{"name":"agent-b","hours":2}`); !strings.Contains(out, "belongs to bob") {
		t.Errorf("alice extended bob's stack: %s", out)
	}
	if out := callAs(t, s, alice, "stack_extend", `{"name":"agent-a","hours":25}`); !strings.Contains(out, `"isError":true`) {
		t.Errorf("alice extended by 25 hours in one call: %s", out)
	}
	out := callAs(t, s, alice, "stack_extend", `{"name":"agent-a","hours":2}`)
	if strings.Contains(out, `"isError":true`) {
		t.Fatalf("alice could not extend her own stack: %s", out)
	}
	rec, err := stack.FindStack("navexa", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.StopAt.Equal(now.Add(3 * time.Hour)) {
		t.Errorf("stop time = %v, want %v", rec.StopAt, now.Add(3*time.Hour))
	}
}
