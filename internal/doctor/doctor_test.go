package doctor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/ports"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

var now = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t      *testing.T
	ws     *workspace.Workspace
	host   Host
	view   *tilt.TiltView
	pruned []string
	gone   []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	parent := t.TempDir()
	ws := &workspace.Workspace{Name: "navexa", Path: filepath.Join(parent, "navexa")}
	if err := os.MkdirAll(ws.Path, 0755); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ws: ws, view: &tilt.TiltView{}}
	f.host = Host{
		Now:        func() time.Time { return now },
		DaemonUp:   func() bool { return true },
		DaemonArgs: func() []string { return []string{"tilt", "up", "--host", "127.0.0.1", "--port", "10300"} },
		View:       func() (*tilt.TiltView, error) { return f.view, nil },
		Listeners:  func([]int) ([]ports.Listener, error) { return nil, nil },
		Owned:      func(int) bool { return true },
		Remote:     func(string, string) (RemoteState, error) { return RemoteState{}, nil },
		Repos:      func() []string { return nil },
		Worktrees:  func(string) ([]Worktree, error) { return nil, nil },
		Dirty:      func(string) (bool, error) { return false, nil },
		Published:  func() (map[int]bool, error) { return nil, nil },
		Replica:    func() (bool, []Drift) { return true, nil },
		RemoveWorktree: func(path string) error {
			f.gone = append(f.gone, path)
			return os.RemoveAll(path)
		},
		Prune: func(repo string) error {
			f.pruned = append(f.pruned, repo)
			return nil
		},
	}
	return f
}

func (f *fixture) store(recs ...stack.Record) {
	f.t.Helper()
	dir := workspace.DataDir(f.ws.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		f.t.Fatal(err)
	}
	data, err := json.Marshal(recs)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(dir+"stacks.json", data, 0644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) stackDir(name string) string {
	f.t.Helper()
	dir := filepath.Join(stack.StacksRoot(f.ws), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		f.t.Fatal(err)
	}
	age(f.t, dir)
	return dir
}

func (f *fixture) ports(key string, ledger map[string]int) string {
	f.t.Helper()
	dir := workspace.DataDir(key)
	if err := os.MkdirAll(dir, 0755); err != nil {
		f.t.Fatal(err)
	}
	data, _ := json.Marshal(ledger)
	path := dir + "ports.json"
	if err := os.WriteFile(path, data, 0644); err != nil {
		f.t.Fatal(err)
	}
	age(f.t, path)
	return path
}

func age(t *testing.T, path string) {
	t.Helper()
	old := now.Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func resource(name, runtime string, port int) tilt.UIResource {
	var r tilt.UIResource
	r.Metadata.Name = name
	r.Status.RuntimeStatus = runtime
	if port > 0 {
		r.Status.EndpointLinks = []tilt.EndpointLink{{URL: "http://localhost:" + strconv.Itoa(port)}}
	}
	return r
}

func (f *fixture) check(stackName string) Report {
	f.t.Helper()
	r, err := Check(f.ws, stackName, f.host)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func kinds(r Report) []string {
	var out []string
	for _, p := range r.Problems {
		out = append(out, p.Kind)
	}
	return out
}

func only(t *testing.T, r Report, kind string, sev Severity) Problem {
	t.Helper()
	if len(r.Problems) != 1 || r.Problems[0].Kind != kind {
		t.Fatalf("want one %s problem, got %v:\n%s", kind, kinds(r), r.Render(false))
	}
	if p := r.Problems[0]; p.Severity != sev {
		t.Errorf("%s has severity %d, want %d", kind, p.Severity, sev)
	}
	return r.Problems[0]
}

func TestHealthyWorkspaceIsOneLine(t *testing.T) {
	f := newFixture(t)
	root := f.stackDir("perf")
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: root, Active: true, Members: []string{"api"}})
	f.view.UiResources = []tilt.UIResource{resource("navexa:api", "ok", 4000), resource("navexa:api:perf", "ok", 20011)}

	if got := f.check("").Render(false); got != "healthy: 2 copies, 1 stacks checked\n" {
		t.Errorf("got %q", got)
	}
}

func TestDaemonDownIsBroken(t *testing.T) {
	f := newFixture(t)
	f.host.DaemonUp = func() bool { return false }
	p := only(t, f.check(""), KindDaemonDown, Broken)
	if p.Fix != "devstack workspace up" {
		t.Errorf("fix = %q", p.Fix)
	}
}

func TestDaemonStillBoundToAllInterfaces(t *testing.T) {
	f := newFixture(t)
	f.host.DaemonArgs = func() []string { return []string{"tilt", "up", "--host", "0.0.0.0", "--port", "10300"} }
	p := only(t, f.check(""), KindDaemonBind, Degraded)
	if !strings.Contains(p.Fix, "devstack workspace down --all && devstack workspace up") {
		t.Errorf("fix = %q", p.Fix)
	}
}

func TestCopyInErrorNamesTheRestart(t *testing.T) {
	f := newFixture(t)
	f.store(stack.Record{Name: "agent-x", Base: "navexa", Root: f.stackDir("agent-x"), Active: true, Members: []string{"api"}})
	f.view.UiResources = []tilt.UIResource{resource("navexa:web", "error", 0), resource("navexa:api:agent-x", "error", 0)}

	r := f.check("")
	if len(r.Problems) != 2 {
		t.Fatalf("got %v", kinds(r))
	}
	base, stk := r.Problems[0], r.Problems[1]
	if base.Fix != "devstack service restart web" || base.Tool != "" {
		t.Errorf("base fix = %q, tool = %q", base.Fix, base.Tool)
	}
	if stk.Fix != "devstack service restart api --stack agent-x" || stk.Tool != `restart(service="api", stack="agent-x")` {
		t.Errorf("stack fix = %q, tool = %q", stk.Fix, stk.Tool)
	}
}

func TestUpStackWithNoCopyInTheDaemon(t *testing.T) {
	f := newFixture(t)
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: f.stackDir("perf"), Active: true, Members: []string{"api"}})
	p := only(t, f.check(""), KindCopyMissing, Broken)
	if p.Subject != "perf:api" || p.Fix != "devstack stack up perf" {
		t.Errorf("got %+v", p)
	}
}

func TestPortClashIgnoresOurOwnListeners(t *testing.T) {
	f := newFixture(t)
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: f.stackDir("perf")})
	f.ports("navexa--perf", map[string]int{"api/http": 20011, "web/http": 20012})
	f.host.Listeners = func(want []int) ([]ports.Listener, error) {
		return []ports.Listener{{Port: 20011, PID: 42, Command: "node"}, {Port: 20012, PID: 7, Command: "tilt"}, {Port: 20012, Unidentified: true}}, nil
	}
	f.host.Owned = func(pid int) bool { return pid == 7 }

	p := only(t, f.check(""), KindPortClash, Broken)
	if !strings.Contains(p.Plain, "stack perf (api/http)") || !strings.Contains(p.Plain, "node (pid 42)") || p.Fix != "devstack ports free 20011" {
		t.Errorf("got %+v", p)
	}
}

func TestRecordWhoseDirectoryIsGone(t *testing.T) {
	f := newFixture(t)
	gone := filepath.Join(stack.StacksRoot(f.ws), "old")
	f.store(stack.Record{Name: "old", Base: "navexa", Root: gone})
	if p := only(t, f.check(""), KindStaleRecord, Tidy); p.Fix != "devstack cleanup --apply" {
		t.Errorf("fix = %q", p.Fix)
	}

	f.store(stack.Record{Name: "old", Base: "navexa", Root: gone, Active: true})
	f.view.UiResources = nil
	r := f.check("")
	if r.Problems[0].Kind != KindStaleRecord || r.Problems[0].Severity != Broken {
		t.Errorf("an up stack with no directory: got %v", kinds(r))
	}
}

func TestWorktreeOfAStackIsGone(t *testing.T) {
	f := newFixture(t)
	root := f.stackDir("perf")
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: root, Worktrees: map[string]string{"api": filepath.Join(root, "api")}})
	p := only(t, f.check(""), KindWorktreeMissing, Broken)
	if p.Subject != "stack perf:api" || p.Tool != `stack_rm(name="perf")` {
		t.Errorf("got %+v", p)
	}
}

func TestStackBranchGoneFromOrigin(t *testing.T) {
	f := newFixture(t)
	root := f.stackDir("perf")
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: root, Branch: "perf", Worktrees: map[string]string{"api": root}})
	f.host.Remote = func(dir, branch string) (RemoteState, error) { return RemoteState{Repo: dir, Gone: true}, nil }
	if p := only(t, f.check(""), KindBranchGone, Tidy); p.Fix != "devstack stack rm perf" {
		t.Errorf("fix = %q", p.Fix)
	}
}

func TestStackBehindOriginSuggestsResync(t *testing.T) {
	f := newFixture(t)
	root := f.stackDir("perf")
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: root, Branch: "perf", Worktrees: map[string]string{"api": root, "worker": root}})
	calls := 0
	f.host.Remote = func(dir, branch string) (RemoteState, error) {
		calls++
		return RemoteState{Repo: dir, Behind: true}, nil
	}
	p := only(t, f.check(""), KindBranchBehind, Degraded)
	if p.Tool != `stack_resync(name="perf")` || calls != 1 {
		t.Errorf("got %+v after %d remote calls", p, calls)
	}
}

func TestRunningCopyWithNoTailnetAddress(t *testing.T) {
	f := newFixture(t)
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: f.stackDir("perf"), Active: true, Members: []string{"api"}})
	f.view.UiResources = []tilt.UIResource{resource("navexa:api", "ok", 4000), resource("navexa:api:perf", "ok", 20011), resource("navexa:db", "ok", 5432)}
	f.host.Published = func() (map[int]bool, error) { return map[int]bool{4000: true}, nil }

	p := only(t, f.check(""), KindUnpublished, Degraded)
	if p.Subject != "perf:api" || p.Fix != "devstack hooks run stack.up --stack perf" {
		t.Errorf("got %+v", p)
	}

	f.host.Published = func() (map[int]bool, error) { return nil, errors.New("no tailscale") }
	if r := f.check(""); len(r.Problems) != 0 {
		t.Errorf("an unreadable tailnet map reported %v", kinds(r))
	}
}

func TestOrphanPortAllocation(t *testing.T) {
	f := newFixture(t)
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: f.stackDir("perf")})
	f.ports("navexa--perf", map[string]int{"api/http": 20011})
	f.ports("navexa--gone", map[string]int{"api/http": 20013})
	f.ports("other--gone", map[string]int{"api/http": 20014})
	if p := only(t, f.check(""), KindOrphanPorts, Tidy); p.Subject != "navexa--gone" {
		t.Errorf("got %+v", p)
	}
}

func TestOrphanDirectoryAndItsWorktrees(t *testing.T) {
	f := newFixture(t)
	f.store(stack.Record{Name: "perf", Base: "navexa", Root: f.stackDir("perf")})
	orphan := f.stackDir("lost")
	f.host.Repos = func() []string { return []string{"/src/api"} }
	f.host.Worktrees = func(string) ([]Worktree, error) {
		return []Worktree{{Path: "/src/api"}, {Path: filepath.Join(orphan, "api")}, {Path: filepath.Join(stack.StacksRoot(f.ws), "perf", "api")}}, nil
	}

	p := only(t, f.check(""), KindOrphanDir, Tidy)
	if p.Subject != orphan || !strings.Contains(p.Plain, "holds 1 git worktrees") {
		t.Errorf("got %+v", p)
	}

	f.host.Dirty = func(string) (bool, error) { return true, nil }
	p = only(t, f.check(""), KindOrphanDir, Tidy)
	if !strings.Contains(p.Plain, "uncommitted work") || !strings.HasPrefix(p.Fix, "git -C "+filepath.Join(orphan, "api")+" stash") {
		t.Errorf("got %+v", p)
	}
}

func TestYoungDirectoryIsNotAnOrphan(t *testing.T) {
	f := newFixture(t)
	f.store()
	dir := filepath.Join(stack.StacksRoot(f.ws), "being-created")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, now, now); err != nil {
		t.Fatal(err)
	}
	if r := f.check(""); len(r.Problems) != 0 {
		t.Errorf("got %v", kinds(r))
	}
}

func TestPrunableWorktree(t *testing.T) {
	f := newFixture(t)
	f.store()
	f.host.Repos = func() []string { return []string{"/src/api"} }
	f.host.Worktrees = func(string) ([]Worktree, error) {
		return []Worktree{{Path: "/gone/a", Prunable: true}, {Path: "/gone/b", Prunable: true}}, nil
	}
	if p := only(t, f.check(""), KindPrunableWorktree, Tidy); p.Subject != "/src/api" || !strings.Contains(p.Plain, "2 worktrees") {
		t.Errorf("got %+v", p)
	}
}

func TestReplicaMissingAndDrift(t *testing.T) {
	f := newFixture(t)
	f.store()
	f.host.Replica = func() (bool, []Drift) { return false, nil }
	only(t, f.check(""), KindReplicaMissing, Degraded)

	f.host.Replica = func() (bool, []Drift) {
		return true, []Drift{{"web", "the replica declares this service, and the workspace does not: /r/web"}}
	}
	if p := only(t, f.check(""), KindReplicaDrift, Degraded); p.Fix != "devstack shared sync" {
		t.Errorf("got %+v", p)
	}
}

func TestStackScopeChecksOnlyThatStack(t *testing.T) {
	f := newFixture(t)
	f.store(
		stack.Record{Name: "agent-a", Base: "navexa", Root: f.stackDir("agent-a"), Active: true, Members: []string{"api"}},
		stack.Record{Name: "perf", Base: "navexa", Root: f.stackDir("perf"), Active: true, Members: []string{"api"}},
	)
	f.stackDir("lost")
	f.host.Replica = func() (bool, []Drift) { return false, nil }
	f.view.UiResources = []tilt.UIResource{resource("navexa:web", "error", 0), resource("navexa:api:agent-a", "ok", 0)}

	r := f.check("AGENT-A")
	if len(r.Problems) != 0 || r.Copies != 1 || r.Stacks != 1 {
		t.Errorf("got %v, %d copies, %d stacks", kinds(r), r.Copies, r.Stacks)
	}
	if _, err := Check(f.ws, "nope", f.host); err == nil || !strings.Contains(err.Error(), `stack "nope" does not exist`) {
		t.Errorf("unknown stack: err = %v", err)
	}
}

func TestRenderGroupsBySeverity(t *testing.T) {
	r := Report{Problems: []Problem{
		{Kind: KindCopyError, Severity: Broken, Subject: "agent-x:api", Plain: "This copy is in error. Its logs say why.", Fix: "devstack service restart api --stack agent-x", Tool: `restart(service="api", stack="agent-x")`},
		{Kind: KindDaemonBind, Severity: Degraded, Subject: "host daemon", Plain: "The host daemon still listens on 0.0.0.0, so any machine on the network can reach it.", Fix: "devstack workspace down --all && devstack workspace up"},
		{Kind: KindOrphanPorts, Severity: Tidy, Subject: "navexa--gone", Plain: "The port allocation belongs to no stack.", Fix: "devstack cleanup --apply"},
	}}
	want := `broken (1)
- agent-x:api: This copy is in error. Its logs say why.
  fix: devstack service restart api --stack agent-x

degraded (1)
- host daemon: The host daemon still listens on 0.0.0.0, so any machine on the network can reach it.
  fix: devstack workspace down --all && devstack workspace up

tidy-up (1)
- navexa--gone: The port allocation belongs to no stack.
  fix: devstack cleanup --apply
`
	if got := r.Render(false); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	agent := r.Render(true)
	for _, line := range []string{
		`  fix: restart(service="api", stack="agent-x")`,
		"  fix (the owner runs it in a shell): devstack cleanup --apply",
	} {
		if !strings.Contains(agent, line) {
			t.Errorf("the agent view lacks %q:\n%s", line, agent)
		}
	}
}
