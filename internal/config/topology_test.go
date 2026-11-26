package config

import (
	"path/filepath"
	"strings"
	"testing"
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
	if len(frontend.Calls) != 1 || frontend.Calls[0] != "api" {
		t.Fatalf("frontend calls = %#v", frontend.Calls)
	}
	if len(frontend.Groups) != 1 || frontend.Groups[0] != "frontend" {
		t.Fatalf("frontend groups = %#v", frontend.Groups)
	}

	api := graph.Services["api"]
	if len(api.CalledBy) != 1 || api.CalledBy[0] != "frontend" {
		t.Fatalf("api called by = %#v", api.CalledBy)
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

func TestBuildTopologyDetectsMissingReferences(t *testing.T) {
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
}
