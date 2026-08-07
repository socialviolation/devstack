package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOverlaySetReuseRule(t *testing.T) {
	dir := writeTopoWorkspace(t, []string{"frontend", "backend", "db", "logger"}, `calls:
  frontend:
    - backend
  backend:
    - db
startsAfter:
  logger:
    - backend
`)

	graph, err := BuildTopology(dir)
	if err != nil {
		t.Fatalf("BuildTopology(): %v", err)
	}

	overlay, reasons, err := OverlaySet(graph, []string{"backend"})
	if err != nil {
		t.Fatalf("OverlaySet(): %v", err)
	}
	want := []string{"backend", "db", "frontend"}
	if !reflect.DeepEqual(overlay, want) {
		t.Fatalf("OverlaySet(backend) = %#v, want %#v (frontend calls backend transitively; backend needs db; logger only starts after, so it stays with base)", overlay, want)
	}
	wantReasons := map[string]string{
		"backend":  OverlayReasonChanged,
		"frontend": OverlayReasonCaller,
		"db":       OverlayReasonNeeded,
	}
	if !reflect.DeepEqual(reasons, wantReasons) {
		t.Fatalf("OverlaySet(backend) reasons = %#v, want %#v", reasons, wantReasons)
	}
}

func TestOverlaySetPullsWhatACallerNeeds(t *testing.T) {
	dir := writeTopoWorkspace(t, []string{"frontend", "backend", "cache"}, `calls:
  frontend:
    - backend
startsAfter:
  frontend:
    - cache
`)

	graph, err := BuildTopology(dir)
	if err != nil {
		t.Fatalf("BuildTopology(): %v", err)
	}

	overlay, _, err := OverlaySet(graph, []string{"backend"})
	if err != nil {
		t.Fatalf("OverlaySet(): %v", err)
	}
	want := []string{"backend", "cache", "frontend"}
	if !reflect.DeepEqual(overlay, want) {
		t.Fatalf("OverlaySet(backend) = %#v, want %#v (frontend comes in as a caller, and it needs cache to start)", overlay, want)
	}
}

func TestOverlaySetNoCallers(t *testing.T) {
	dir := writeTopoWorkspace(t, []string{"frontend", "backend"}, `calls:
  frontend:
    - backend
`)

	graph, err := BuildTopology(dir)
	if err != nil {
		t.Fatalf("BuildTopology(): %v", err)
	}

	overlay, _, err := OverlaySet(graph, []string{"frontend"})
	if err != nil {
		t.Fatalf("OverlaySet(): %v", err)
	}
	want := []string{"backend", "frontend"}
	if !reflect.DeepEqual(overlay, want) {
		t.Fatalf("OverlaySet(frontend) = %#v, want %#v (frontend has no callers, and it calls backend)", overlay, want)
	}
}

func TestOverlaySetUnknownService(t *testing.T) {
	dir := writeTopoWorkspace(t, []string{"frontend"}, "")

	graph, err := BuildTopology(dir)
	if err != nil {
		t.Fatalf("BuildTopology(): %v", err)
	}

	if _, _, err := OverlaySet(graph, []string{"ghost"}); err == nil {
		t.Fatal("expected an error for an unknown changed service")
	}
}

func baseWorkspace() *ResolvedWorkspace {
	return &ResolvedWorkspace{
		Manifest: &WorkspaceManifest{
			Version: 1,
			Workspace: WorkspaceManifestWorkspace{
				Name: "navexa",
			},
			Runtime: WorkspaceManifestRuntime{
				Orchestrator: "tilt",
				Infra: WorkspaceManifestInfra{
					Provider:     "compose",
					ComposeFiles: []string{"docker-compose.yaml"},
				},
			},
			Env: WorkspaceManifestEnv{
				Values: map[string]string{"REGION": "au"},
				Files:  []string{".env.shared"},
			},
			Calls: map[string][]string{
				"frontend": {"backend"},
				"backend":  {"db"},
			},
			StartsAfter: map[string][]string{
				"logger": {"backend"},
			},
			Dependencies: map[string][]string{
				"frontend": {"backend"},
			},
			Groups: map[string][]string{
				"app":   {"frontend", "backend"},
				"infra": {"db"},
			},
		},
	}
}

func pathForFunc(m map[string]string) func(string) string {
	return func(s string) string { return m[s] }
}

func TestGenerateStackManifestReposFilteredToOverlay(t *testing.T) {
	base := baseWorkspace()
	paths := map[string]string{
		"frontend": "/wt/frontend",
		"backend":  "/wt/backend",
		"db":       "/wt/db",
	}

	m, err := GenerateStackManifest(base, "navexa--import-review", []string{"backend", "frontend"}, pathForFunc(paths))
	if err != nil {
		t.Fatalf("GenerateStackManifest(): %v", err)
	}

	if m.Workspace.Name != "navexa--import-review" {
		t.Fatalf("Workspace.Name = %q, want the passed stack name", m.Workspace.Name)
	}
	if m.Workspace.RepoDiscovery.Mode != RepoDiscoveryModeExplicit {
		t.Fatalf("RepoDiscovery.Mode = %q, want explicit", m.Workspace.RepoDiscovery.Mode)
	}
	wantRepos := []string{"/wt/backend", "/wt/frontend"}
	if !reflect.DeepEqual(m.Workspace.RepoDiscovery.Repos, wantRepos) {
		t.Fatalf("Repos = %#v, want exactly the overlay worktree paths %#v", m.Workspace.RepoDiscovery.Repos, wantRepos)
	}
}

func TestGenerateStackManifestOmitsInfra(t *testing.T) {
	base := baseWorkspace()
	paths := map[string]string{"backend": "/wt/backend"}

	m, err := GenerateStackManifest(base, "navexa--x", []string{"backend"}, pathForFunc(paths))
	if err != nil {
		t.Fatalf("GenerateStackManifest(): %v", err)
	}

	if !reflect.DeepEqual(m.Runtime.Infra, WorkspaceManifestInfra{}) {
		t.Fatalf("Runtime.Infra = %#v, want zero (stack reuses base infra)", m.Runtime.Infra)
	}
	if m.Runtime.Orchestrator != "tilt" {
		t.Fatalf("Runtime.Orchestrator = %q, want it carried over from base", m.Runtime.Orchestrator)
	}

	out, err := yaml.Marshal(m)
	if err != nil {
		t.Fatalf("yaml.Marshal(): %v", err)
	}
	if strings.Contains(string(out), "infra") {
		t.Fatalf("serialized stack manifest still contains runtime.infra:\n%s", out)
	}
	if strings.Contains(string(out), "compose") {
		t.Fatalf("serialized stack manifest still references compose:\n%s", out)
	}
}

func TestGenerateStackManifestFiltersDependencyEdges(t *testing.T) {
	base := baseWorkspace()
	paths := map[string]string{"backend": "/wt/backend", "frontend": "/wt/frontend"}

	m, err := GenerateStackManifest(base, "navexa--x", []string{"backend", "frontend"}, pathForFunc(paths))
	if err != nil {
		t.Fatalf("GenerateStackManifest(): %v", err)
	}

	if got, ok := m.Calls["frontend"]; !ok || !reflect.DeepEqual(got, []string{"backend"}) {
		t.Fatalf("Calls[frontend] = %#v (ok=%v), want [backend] (edge among overlay services kept)", got, ok)
	}
	if got, ok := m.Calls["backend"]; ok {
		t.Fatalf("Calls[backend] = %#v, want dropped: db is not in the overlay so backend->db is a dangling edge", got)
	}
	if _, ok := m.StartsAfter["logger"]; ok {
		t.Fatal("StartsAfter[logger] should be dropped: logger is not an overlay service")
	}
	if got, ok := m.Groups["app"]; !ok || !reflect.DeepEqual(got, []string{"frontend", "backend"}) {
		t.Fatalf("Groups[app] = %#v (ok=%v), want [frontend backend] filtered to overlay (input order preserved)", got, ok)
	}
	if _, ok := m.Groups["infra"]; ok {
		t.Fatal("Groups[infra] should be dropped: its only member db is not in the overlay")
	}
}

func TestGenerateStackManifestCarriesEnv(t *testing.T) {
	base := baseWorkspace()
	paths := map[string]string{"backend": "/wt/backend"}

	m, err := GenerateStackManifest(base, "navexa--x", []string{"backend"}, pathForFunc(paths))
	if err != nil {
		t.Fatalf("GenerateStackManifest(): %v", err)
	}
	if !reflect.DeepEqual(m.Env, base.Manifest.Env) {
		t.Fatalf("Env = %#v, want base workspace env carried through %#v", m.Env, base.Manifest.Env)
	}
}
