package config

import (
	"path/filepath"
	"testing"
)

func TestSetEnvValueRoundTrips(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
`)

	if err := SetEnvValue(dir, "staging", "API_URL", "http://staging"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}
	if err := SetEnvValue(dir, "staging", "REGION", "au"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	env := m.Environments["staging"]
	if got := env.Values["API_URL"]; got != "http://staging" {
		t.Errorf("API_URL = %q, want http://staging", got)
	}
	if got := env.Values["REGION"]; got != "au" {
		t.Errorf("REGION = %q, want au", got)
	}
}

func TestSetWorkspaceEnvRoundTrips(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
`)

	if err := SetWorkspaceEnv(dir, "staging"); err != nil {
		t.Fatalf("SetWorkspaceEnv: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	if got := m.Workspace.Env; got != "staging" {
		t.Errorf("workspace.env = %q, want staging", got)
	}
}

func TestSetServiceEnvRoundTrips(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)

	if err := SetServiceEnv(ServiceManifestPath(dir), "staging"); err != nil {
		t.Fatalf("SetServiceEnv: %v", err)
	}

	m, err := LoadServiceManifest(dir)
	if err != nil {
		t.Fatalf("LoadServiceManifest: %v", err)
	}
	if got := m.Service.Env; got != "staging" {
		t.Errorf("service.env = %q, want staging", got)
	}
}

func TestSetServiceEnvRequiresManifest(t *testing.T) {
	if err := SetServiceEnv(ServiceManifestPath(t.TempDir()), "staging"); err == nil {
		t.Fatal("expected an error when the service has no manifest")
	}
}
