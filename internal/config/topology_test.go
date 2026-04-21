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
	if !strings.Contains(joined, `group "backend" references unknown service "missing"`) {
		t.Fatalf("missing group-member issue in %q", joined)
	}
	if !strings.Contains(joined, "dependency cycle detected") {
		t.Fatalf("missing cycle issue in %q", joined)
	}
}
