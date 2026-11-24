package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

type cleanupFixture struct {
	*fixture
	liveRoot, liveWT, livePorts string
	orphan, orphanWT            string
	orphanPorts                 string
	staleRoot                   string
}

func newCleanupFixture(t *testing.T) *cleanupFixture {
	f := &cleanupFixture{fixture: newFixture(t)}
	f.liveRoot = f.stackDir("perf")
	f.liveWT = filepath.Join(f.liveRoot, "api")
	f.orphan = f.stackDir("lost")
	f.orphanWT = filepath.Join(f.orphan, "api")
	for _, d := range []string{f.liveWT, f.orphanWT} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	age(t, f.orphan)
	f.staleRoot = filepath.Join(stack.StacksRoot(f.ws), "old")
	f.store(
		stack.Record{Name: "perf", Base: "navexa", Root: f.liveRoot, Worktrees: map[string]string{"api": f.liveWT}},
		stack.Record{Name: "old", Base: "navexa", Root: f.staleRoot},
		stack.Record{Name: "up", Base: "navexa", Root: filepath.Join(stack.StacksRoot(f.ws), "up"), Active: true},
	)
	f.livePorts = f.ports("navexa--perf", map[string]int{"api/http": 20011})
	f.ports("navexa--old", map[string]int{"api/http": 20012})
	f.orphanPorts = f.ports("navexa--lost", map[string]int{"api/http": 20013})
	f.host.Repos = func() []string { return []string{"/src/api"} }
	f.host.Worktrees = func(string) ([]Worktree, error) {
		return []Worktree{{Path: "/src/api"}, {Path: f.liveWT}, {Path: f.orphanWT}, {Path: "/gone/api", Prunable: true}}, nil
	}
	return f
}

func exist(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is gone: %v", p, err)
		}
	}
}

func absent(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
}

func recordNames(t *testing.T) string {
	t.Helper()
	recs, err := stack.LoadStore("navexa")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range recs {
		names = append(names, r.Name)
	}
	return strings.Join(names, ",")
}

func TestCleanupDryRunChangesNothing(t *testing.T) {
	f := newCleanupFixture(t)
	actions, err := Cleanup(f.ws, false, f.host)
	if err != nil {
		t.Fatal(err)
	}
	out := RenderCleanup(actions, false, "To do it, run: devstack cleanup --apply")

	exist(t, f.orphan, f.orphanWT, f.orphanPorts, f.livePorts)
	if got := recordNames(t); got != "perf,old,up" {
		t.Errorf("records = %s", got)
	}
	if len(f.gone) != 0 || len(f.pruned) != 0 {
		t.Errorf("dry run removed %v and pruned %v", f.gone, f.pruned)
	}
	for _, want := range []string{
		"- would remove the record of stack old",
		"- would remove the worktree " + f.orphanWT,
		"- would remove the directory " + f.orphan,
		"- would release the port allocation " + f.orphanPorts,
		"- would prune the worktrees git lists in /src/api",
		"dry run: nothing changed. To do it, run: devstack cleanup --apply",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, f.liveRoot+"\n") || strings.Contains(out, "stack up") || strings.Contains(out, "stack perf") {
		t.Errorf("dry run names a live stack:\n%s", out)
	}
}

func TestCleanupApplyRemovesOnlyOrphans(t *testing.T) {
	f := newCleanupFixture(t)
	actions, err := Cleanup(f.ws, true, f.host)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.Err != nil {
			t.Errorf("%s: %v", a.What, a.Err)
		}
	}

	absent(t, f.orphan, f.orphanPorts, workspace.DataDir("navexa--old")+"ports.json")
	exist(t, f.liveRoot, f.liveWT, f.livePorts)
	if got := recordNames(t); got != "perf,up" {
		t.Errorf("records = %s", got)
	}
	if strings.Join(f.gone, ",") != f.orphanWT || strings.Join(f.pruned, ",") != "/src/api" {
		t.Errorf("removed %v, pruned %v", f.gone, f.pruned)
	}
}

func TestCleanupKeepsAnOrphanHoldingUncommittedWork(t *testing.T) {
	f := newCleanupFixture(t)
	f.host.Dirty = func(path string) (bool, error) { return path == f.orphanWT, nil }
	actions, err := Cleanup(f.ws, true, f.host)
	if err != nil {
		t.Fatal(err)
	}
	exist(t, f.orphan, f.orphanWT)
	if out := RenderCleanup(actions, true, ""); !strings.Contains(out, "- keep "+f.orphan+": the worktree "+f.orphanWT+" holds uncommitted work") {
		t.Errorf("got:\n%s", out)
	}
}
