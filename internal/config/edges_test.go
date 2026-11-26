package config

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func writeEdgeWorkspace(t *testing.T, services map[string]string, workspaceExtra string) string {
	t.Helper()
	dir := t.TempDir()
	repos := ""
	for _, name := range sortedNames(services) {
		repos += "\n      - ./services/" + name
		mustWriteFile(t, filepath.Join(dir, "services", name, ServiceManifestFileName), `version: 1
service:
  name: `+name+`
runtime:
  run:
    command: ./bin/`+name+`
`+services[name])
	}
	mustWriteFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: edges
  repoDiscovery:
    mode: explicit
    repos:`+repos+"\n"+workspaceExtra)
	return dir
}

func sortedNames(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return unionSorted(out)
}

func TestServiceEdgeAcceptsStringAndMapForms(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{
		"api": `needs:
  - sql
  - service: queue
    via: queue
    entities: [stage-trades-queue]
calls:
  - service: web
    via: http
  - worker
`,
		"sql": "", "queue": "", "web": "", "worker": "",
	}, "")
	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	wantNeeds := []ServiceEdge{{Service: "sql"}, {Service: "queue", Via: "queue", Entities: []string{"stage-trades-queue"}}}
	if got := rw.Needs("api"); !reflect.DeepEqual(got, wantNeeds) {
		t.Errorf("Needs(api) = %#v, want %#v", got, wantNeeds)
	}
	wantCalls := []ServiceEdge{{Service: "web", Via: "http"}, {Service: "worker"}}
	if got := rw.CallEdges("api"); !reflect.DeepEqual(got, wantCalls) {
		t.Errorf("CallEdges(api) = %#v, want %#v", got, wantCalls)
	}
}

func TestEdgeToAnUndeclaredServiceFailsLoad(t *testing.T) {
	for _, verb := range []string{"needs", "calls"} {
		dir := writeEdgeWorkspace(t, map[string]string{"api": verb + ": [ghost]\n"}, "")
		_, err := ResolveWorkspace(dir)
		if err == nil || !strings.Contains(err.Error(), `"api" `+verb+` the service "ghost"`) {
			t.Errorf("%s: err = %v, want an error naming api and ghost", verb, err)
		}
	}
}

func TestPartialWorkspaceAllowsEdgesToServicesItDoesNotRun(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{"api": "needs: [sql]\ncalls: [web]\n"}, "partial: true\n")
	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if deps := rw.ResourceDeps("api"); len(deps) != 0 {
		t.Errorf("ResourceDeps(api) = %v, want none for a service the workspace does not run", deps)
	}
}

func TestNeedsCycleFailsLoadAndNamesTheCycle(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{
		"a": "needs: [b]\n",
		"b": "needs: [c]\n",
		"c": "needs: [a]\n",
	}, "")
	_, err := ResolveWorkspace(dir)
	if err == nil || !strings.Contains(err.Error(), "a -> b -> c -> a") {
		t.Fatalf("err = %v, want a needs cycle naming a -> b -> c -> a", err)
	}
}

func TestCallsCycleLoads(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{"a": "calls: [b]\n", "b": "calls: [a]\n"}, "")
	if _, err := ResolveWorkspace(dir); err != nil {
		t.Fatalf("a calls cycle must load: %v", err)
	}
}

func TestCallsEdgeAddsNoResourceDeps(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{
		"api":    "needs: [sql]\ncalls: [worker]\n",
		"sql":    "",
		"worker": "",
	}, "")
	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got := rw.ResourceDeps("api"); !reflect.DeepEqual(got, []string{"sql"}) {
		t.Errorf("ResourceDeps(api) = %v, want [sql]", got)
	}
}

func captureLegacyWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := legacyEdgeWarnings
	legacyEdgeWarnings = &buf
	legacyEdgeWarned = sync.Map{}
	t.Cleanup(func() { legacyEdgeWarnings = prev })
	return &buf
}

func TestLegacyMapsLoadAsCallsWithOneWarning(t *testing.T) {
	buf := captureLegacyWarnings(t)
	dir := writeEdgeWorkspace(t, map[string]string{"api": "", "db": "", "worker": "", "web": ""}, `dependencies:
  api: [db]
startsAfter:
  api: [worker]
calls:
  web: [api]
`)
	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if _, err := ResolveWorkspace(dir); err != nil {
		t.Fatalf("second ResolveWorkspace: %v", err)
	}
	if got := rw.ResourceDeps("api"); len(got) != 0 {
		t.Errorf("ResourceDeps(api) = %v, want none: the legacy maps load as calls", got)
	}
	if got := rw.CallEdges("api"); !reflect.DeepEqual(got, []ServiceEdge{{Service: "db"}, {Service: "worker"}}) {
		t.Errorf("CallEdges(api) = %#v, want db and worker", got)
	}
	if got := rw.CallEdges("web"); !reflect.DeepEqual(got, []ServiceEdge{{Service: "api"}}) {
		t.Errorf("CallEdges(web) = %#v, want api", got)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("want exactly one warning line, got %q", out)
	}
	for _, want := range []string{"dependencies, calls, startsAfter", "devstack migrate"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning %q does not contain %q", out, want)
		}
	}
}

func TestNoWarningWithoutLegacyMaps(t *testing.T) {
	buf := captureLegacyWarnings(t)
	dir := writeEdgeWorkspace(t, map[string]string{"api": "needs: [db]\n", "db": ""}, "")
	if _, err := ResolveWorkspace(dir); err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("want no warning, got %q", buf.String())
	}
}

func closureGraph(t *testing.T) *TopologyGraph {
	t.Helper()
	dir := writeEdgeWorkspace(t, map[string]string{
		"frontend": "needs: [api]\ncalls: [search]\n",
		"api":      "needs: [sql, cache]\n",
		"cache":    "needs: [sql]\n",
		"sql":      "needs: [disk]\n",
		"disk":     "",
		"search":   "",
	}, "")
	g, err := BuildTopology(dir)
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	return g
}

func TestNeedsClosureAddsTransitiveNeedsWithChains(t *testing.T) {
	members, chains := closureGraph(t).NeedsClosure([]string{"frontend"}, nil)
	if want := []string{"api", "cache", "disk", "frontend", "sql"}; !reflect.DeepEqual(members, want) {
		t.Errorf("members = %v, want %v", members, want)
	}
	wantChains := map[string][]string{
		"api":   {"frontend", "api"},
		"cache": {"frontend", "api", "cache"},
		"sql":   {"frontend", "api", "sql"},
		"disk":  {"frontend", "api", "sql", "disk"},
	}
	if !reflect.DeepEqual(chains, wantChains) {
		t.Errorf("chains = %v, want %v", chains, wantChains)
	}
}

func TestNeedsClosureDoesNotWalkExcludedServices(t *testing.T) {
	members, chains := closureGraph(t).NeedsClosure([]string{"frontend", "sql"}, map[string]bool{"sql": true})
	if want := []string{"api", "cache", "frontend"}; !reflect.DeepEqual(members, want) {
		t.Errorf("members = %v, want %v", members, want)
	}
	if _, ok := chains["disk"]; ok {
		t.Errorf("disk is only needed through the excluded sql; chains = %v", chains)
	}
}

func TestTransitiveCallersSkipsLegacyDependencies(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{"web": "", "api": "", "worker": "calls: [api]\n"}, `dependencies:
  web: [api]
`)
	g, err := BuildTopology(dir)
	if err != nil {
		t.Fatalf("BuildTopology: %v", err)
	}
	if got := g.TransitiveCallers("api"); !reflect.DeepEqual(got, []string{"worker"}) {
		t.Errorf("TransitiveCallers(api) = %v, want [worker]: a legacy dependency must not grow the overlay", got)
	}
}
