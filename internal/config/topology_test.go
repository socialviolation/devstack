package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildTopologyGroupsDependenciesAndDependents(t *testing.T) {
	workspaceDir := t.TempDir()
	mustWriteFile(t, filepath.Join(workspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./services/frontend
      - ./services/api
      - ./services/worker
groups:
  frontend:
    - frontend
  backend:
    - api
    - worker
dependencies:
  frontend:
    - api
  api:
    - worker
`)
	mustWriteFile(t, filepath.Join(workspaceDir, "services", "frontend", ServiceManifestFileName), `version: 1
service:
  name: frontend
runtime:
  run:
    command: npm run dev
`)
	mustWriteFile(t, filepath.Join(workspaceDir, "services", "api", ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)
	mustWriteFile(t, filepath.Join(workspaceDir, "services", "worker", ServiceManifestFileName), `version: 1
service:
  name: worker
runtime:
  run:
    command: go run ./cmd/worker
`)

	graph, err := BuildTopology(workspaceDir)
	if err != nil {
		t.Fatalf("BuildTopology(): %v", err)
	}
	if graph.HasErrors() {
		t.Fatalf("BuildTopology() returned unexpected issues: %#v", graph.Issues)
	}

	frontend := graph.Services["frontend"]
	if len(frontend.Dependencies) != 1 || frontend.Dependencies[0] != "api" {
		t.Fatalf("frontend dependencies = %#v", frontend.Dependencies)
	}
	if len(frontend.Groups) != 1 || frontend.Groups[0] != "frontend" {
		t.Fatalf("frontend groups = %#v", frontend.Groups)
	}

	api := graph.Services["api"]
	if len(api.Dependents) != 1 || api.Dependents[0] != "frontend" {
		t.Fatalf("api dependents = %#v", api.Dependents)
	}
	if len(api.Groups) != 1 || api.Groups[0] != "backend" {
		t.Fatalf("api groups = %#v", api.Groups)
	}
}

func writeTopoWorkspace(t *testing.T, services []string, edges string) string {
	t.Helper()
	dir := t.TempDir()
	repos := ""
	for _, name := range services {
		repos += "\n      - ./services/" + name
		mustWriteFile(t, filepath.Join(dir, "services", name, ServiceManifestFileName), `version: 1
service:
  name: `+name+`
runtime:
  run:
    command: ./bin/`+name+`
`)
	}
	mustWriteFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:`+repos+"\n"+edges)
	return dir
}

func TestTransitiveCallersIgnoresStartOrderEdges(t *testing.T) {
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
	if graph.HasErrors() {
		t.Fatalf("unexpected issues: %#v", graph.Issues)
	}

	if got := graph.Services["db"].CalledBy; len(got) != 1 || got[0] != "backend" {
		t.Fatalf("db.CalledBy = %#v, want [backend]", got)
	}
	if got := graph.Services["backend"].CalledBy; len(got) != 1 || got[0] != "frontend" {
		t.Fatalf("backend.CalledBy = %#v, want [frontend]", got)
	}

	if got := graph.Services["backend"].Dependents; !contains(got, "logger") {
		t.Fatalf("backend.Dependents = %#v, want it to include logger (start-order edge)", got)
	}
	if got := graph.Services["backend"].CalledBy; contains(got, "logger") {
		t.Fatalf("logger is a start-order dependent, not a caller; backend.CalledBy = %#v", got)
	}

	callers := graph.TransitiveCallers("db")
	if len(callers) != 2 || callers[0] != "backend" || callers[1] != "frontend" {
		t.Fatalf("TransitiveCallers(db) = %#v, want [backend frontend] and NOT logger", callers)
	}
}

func TestTransitiveCallersTerminatesOnCycle(t *testing.T) {
	dir := writeTopoWorkspace(t, []string{"a", "b"}, `calls:
  a:
    - b
  b:
    - a
`)

	graph, err := BuildTopology(dir)
	if err != nil {
		t.Fatalf("BuildTopology(): %v", err)
	}

	done := make(chan []string, 1)
	go func() { done <- graph.TransitiveCallers("a") }()
	select {
	case callers := <-done:
		if len(callers) != 2 || callers[0] != "a" || callers[1] != "b" {
			t.Fatalf("TransitiveCallers(a) = %#v, want [a b]", callers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TransitiveCallers did not terminate on a cyclic call graph")
	}
}

func TestBuildTopologyFlagsUnknownCall(t *testing.T) {
	dir := writeTopoWorkspace(t, []string{"frontend"}, `calls:
  frontend:
    - ghost
`)

	graph, err := BuildTopology(dir)
	if err != nil {
		t.Fatalf("BuildTopology(): %v", err)
	}
	if !graph.HasErrors() {
		t.Fatal("expected a topology issue for a call to an unknown service")
	}
	var joined string
	for _, issue := range graph.Issues {
		joined += issue.Message + "\n"
	}
	if !strings.Contains(joined, `unknown service "ghost"`) {
		t.Fatalf("missing unknown-call issue in %q", joined)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestBuildTopologyDetectsMissingReferencesAndCycles(t *testing.T) {
	workspaceDir := t.TempDir()
	mustWriteFile(t, filepath.Join(workspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: broken
  repoDiscovery:
    mode: explicit
    repos:
      - ./services/api
      - ./services/worker
groups:
  backend:
    - api
    - missing
dependencies:
  api:
    - worker
  worker:
    - api
  web:
    - api
`)
	mustWriteFile(t, filepath.Join(workspaceDir, "services", "api", ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)
	mustWriteFile(t, filepath.Join(workspaceDir, "services", "worker", ServiceManifestFileName), `version: 1
service:
  name: worker
runtime:
  run:
    command: go run ./cmd/worker
`)

	graph, err := BuildTopology(workspaceDir)
	if err != nil {
		t.Fatalf("BuildTopology(): %v", err)
	}
	if !graph.HasErrors() {
		t.Fatal("BuildTopology() returned no errors for broken graph")
	}

	var messages []string
	for _, issue := range graph.Issues {
		messages = append(messages, issue.Message)
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, `group "backend" references the unknown service "missing"`) {
		t.Fatalf("missing group-member issue in %q", joined)
	}
	if !strings.Contains(joined, "are in a dependency cycle") {
		t.Fatalf("missing cycle issue in %q", joined)
	}
}
