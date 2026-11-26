package herdr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

type fakeHerdr struct {
	panes []Pane
	calls [][]string
}

func (f *fakeHerdr) run(bin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if len(args) >= 2 && args[0] == "pane" && args[1] == "list" {
		return json.Marshal(map[string]any{"id": "cli:pane:list", "result": map[string]any{"type": "pane_list", "panes": f.panes}})
	}
	return nil, nil
}

func (f *fakeHerdr) reports() map[string][]string {
	out := map[string][]string{}
	for _, c := range f.calls {
		if len(c) > 2 && c[1] == "report-metadata" {
			out[c[2]] = c[3:]
		}
	}
	return out
}

func installHerdr(t *testing.T, panes ...string) *fakeHerdr {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	sock := filepath.Join(dir, "herdr.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", "/usr/bin/herdr-fake")
	t.Setenv("HERDR_SOCKET_PATH", sock)
	f := &fakeHerdr{}
	for _, p := range panes {
		f.panes = append(f.panes, Pane{ID: p})
	}
	prevRun, prevLink := Run, LinkFor
	Run = f.run
	LinkFor = func(context.Context) func(int) string {
		return func(port int) string {
			if port == 20001 {
				return "https://omarchy.example.ts.net:8401"
			}
			return ""
		}
	}
	t.Cleanup(func() { Run, LinkFor = prevRun, prevLink })
	return f
}

func registerStore(t *testing.T, name string, recs ...stack.Record) {
	t.Helper()
	if err := workspace.Register(workspace.Workspace{Name: name, Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	dir := workspace.DataDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"stacks.json", data, 0o644); err != nil {
		t.Fatal(err)
	}
}

var t0 = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

func TestPublishSetsTokensForEachState(t *testing.T) {
	f := installHerdr(t, "w1:p1", "w1:p2", "w1:p3", "w1:p4")
	stopAt := t0.Add(stack.StopAfter)
	registerStore(t, "navexa",
		stack.Record{Name: "up-web", Base: "navexa", Pane: "w1:p1", Active: true, CreatedAt: t0, StopAt: stopAt, Ports: map[string]int{"api/http": 20002, "web/http": 20001}},
		stack.Record{Name: "up-api", Base: "navexa", Pane: "w1:p2", Active: true, CreatedAt: t0, StopAt: stopAt, Ports: map[string]int{"api/http": 20002}},
		stack.Record{Name: "halted", Base: "navexa", Pane: "w1:p3", CreatedAt: t0, StopAt: stopAt, StoppedBy: stack.StoppedByUser},
		stack.Record{Name: "expired", Base: "navexa", Pane: "w1:p4", CreatedAt: t0, StopAt: stopAt, StoppedBy: stack.StoppedBySweep},
	)

	if err := Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	stop := stopAt.Local().Format("15:04")
	want := map[string][]string{
		"w1:p1": {"--source", "devstack", "--token", "stack=up-web", "--token", "stack_state=up", "--token", "stack_url=https://omarchy.example.ts.net:8401", "--token", "stack_stop=" + stop},
		"w1:p2": {"--source", "devstack", "--token", "stack=up-api", "--token", "stack_state=up", "--clear-token", "stack_url", "--token", "stack_stop=" + stop},
		"w1:p3": {"--source", "devstack", "--token", "stack=halted", "--token", "stack_state=stopped", "--clear-token", "stack_url", "--clear-token", "stack_stop"},
		"w1:p4": {"--source", "devstack", "--token", "stack=expired", "--token", "stack_state=stopped-auto", "--clear-token", "stack_url", "--clear-token", "stack_stop"},
	}
	if got := f.reports(); !reflect.DeepEqual(got, want) {
		t.Errorf("reports:\n got %q\nwant %q", got, want)
	}
}

func TestPublishNamesTheLatestStackAndCountsTheRest(t *testing.T) {
	f := installHerdr(t, "w1:p1")
	registerStore(t, "navexa",
		stack.Record{Name: "old", Base: "navexa", Pane: "w1:p1", CreatedAt: t0, StopAt: t0.Add(stack.StopAfter)},
		stack.Record{Name: "newer", Base: "navexa", Pane: "w1:p1", CreatedAt: t0, Active: true, StopAt: t0.Add(2 * time.Hour).Add(stack.StopAfter)},
	)
	registerStore(t, "other",
		stack.Record{Name: "mid", Base: "other", Pane: "w1:p1", CreatedAt: t0.Add(time.Hour), StopAt: t0.Add(time.Hour).Add(stack.StopAfter)},
	)

	if err := Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(f.reports()["w1:p1"], " ")
	if !strings.Contains(args, "--token stack=newer +2") || !strings.Contains(args, "--token stack_state=up") {
		t.Errorf("report for a pane with three stacks: %s", args)
	}
}

func TestPublishClearsTheBindingOfAPaneHerdrNoLongerHas(t *testing.T) {
	f := installHerdr(t, "w1:p1")
	registerStore(t, "navexa",
		stack.Record{Name: "kept", Base: "navexa", Pane: "w1:p1", CreatedAt: t0},
		stack.Record{Name: "stale", Base: "navexa", Pane: "w9:gone", CreatedAt: t0},
	)

	if err := Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	stale, err := stack.FindStack("navexa", "stale")
	if err != nil {
		t.Fatal(err)
	}
	if stale.Pane != "" {
		t.Errorf("stale binding kept: pane %q", stale.Pane)
	}
	kept, _ := stack.FindStack("navexa", "kept")
	if kept.Pane != "w1:p1" {
		t.Errorf("live binding lost: pane %q", kept.Pane)
	}
	if _, ok := f.reports()["w9:gone"]; ok {
		t.Errorf("devstack reported to a pane herdr does not have")
	}
}

func TestPublishKeepsEveryBindingWhenHerdrListsNoPanes(t *testing.T) {
	installHerdr(t)
	registerStore(t, "navexa", stack.Record{Name: "kept", Base: "navexa", Pane: "w1:p1", CreatedAt: t0})

	if err := Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if kept, _ := stack.FindStack("navexa", "kept"); kept.Pane != "w1:p1" {
		t.Errorf("an empty pane list cleared the binding: pane %q", kept.Pane)
	}
}

func TestPublishClearsTokensFromAPaneThatBindsNoStack(t *testing.T) {
	f := installHerdr(t)
	f.panes = []Pane{
		{ID: "w1:p1", Tokens: map[string]string{"ctx": "40%", "stack": "gone"}},
		{ID: "w1:p2", Tokens: map[string]string{"ctx": "10%"}},
	}
	registerStore(t, "navexa")

	if err := Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"w1:p1": {"--source", "devstack", "--clear-token", "stack", "--clear-token", "stack_state", "--clear-token", "stack_url", "--clear-token", "stack_stop"},
	}
	if got := f.reports(); !reflect.DeepEqual(got, want) {
		t.Errorf("reports:\n got %q\nwant %q", got, want)
	}
}

func TestPublishWithoutHerdrDoesNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		env  func(t *testing.T)
	}{
		{"no binary", func(t *testing.T) {
			t.Setenv("HERDR_BIN_PATH", "")
			t.Setenv("PATH", t.TempDir())
		}},
		{"no socket", func(t *testing.T) {
			t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "missing.sock"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := installHerdr(t, "w1:p1")
			registerStore(t, "navexa", stack.Record{Name: "a", Base: "navexa", Pane: "w1:p1", CreatedAt: t0})
			c.env(t)
			if err := Publish(context.Background()); err != nil {
				t.Errorf("Publish: %v, want nil", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("herdr ran: %q", f.calls)
			}
		})
	}
}

func TestBindRecordsThePaneOfThisProcess(t *testing.T) {
	installHerdr(t)
	registerStore(t, "navexa", stack.Record{Name: "a", Base: "navexa", Pane: "w1:old"})

	t.Setenv("HERDR_PANE_ID", "")
	Bind("navexa", "a")
	if rec, _ := stack.FindStack("navexa", "a"); rec.Pane != "w1:old" {
		t.Errorf("no pane id: pane %q, want w1:old", rec.Pane)
	}

	t.Setenv("HERDR_PANE_ID", "w2:new")
	Bind("navexa", "a")
	if rec, _ := stack.FindStack("navexa", "a"); rec.Pane != "w2:new" {
		t.Errorf("pane %q, want w2:new", rec.Pane)
	}
}

func TestContextPaneReadsThePluginContext(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:env")
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"focused_pane_id":"w1:ctx","tab_id":"w1:t1"}`)
	if got := ContextPane(); got != "w1:ctx" {
		t.Errorf("ContextPane = %q, want w1:ctx", got)
	}
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")
	if got := ContextPane(); got != "w1:env" {
		t.Errorf("ContextPane = %q, want w1:env", got)
	}
}

func TestPaneURLIsTheURLOfThePaneStack(t *testing.T) {
	installHerdr(t)
	registerStore(t, "navexa",
		stack.Record{Name: "web", Base: "navexa", Pane: "w1:p1", Active: true, CreatedAt: t0, StopAt: t0.Add(stack.StopAfter), Ports: map[string]int{"web/http": 20001}},
		stack.Record{Name: "down", Base: "navexa", Pane: "w1:p2", CreatedAt: t0, Ports: map[string]int{"web/http": 20001}},
	)
	if got, err := PaneURL(context.Background(), "w1:p1"); err != nil || got != "https://omarchy.example.ts.net:8401" {
		t.Errorf("PaneURL(w1:p1) = %q, %v", got, err)
	}
	if _, err := PaneURL(context.Background(), "w1:p2"); err == nil {
		t.Errorf("PaneURL of a stack that is down: no error")
	}
	if _, err := PaneURL(context.Background(), "w1:none"); err == nil {
		t.Errorf("PaneURL of a pane with no stack: no error")
	}
}
