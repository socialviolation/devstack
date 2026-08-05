package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetServiceEnvValueCreatesEnvValues(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)

	if err := SetServiceEnvValue(ServiceManifestPath(dir), "NAVEXA_API_URL", "http://localhost:8080"); err != nil {
		t.Fatalf("SetServiceEnvValue: %v", err)
	}

	m, err := LoadServiceManifest(dir)
	if err != nil {
		t.Fatalf("LoadServiceManifest: %v", err)
	}
	if got := m.Env.Values["NAVEXA_API_URL"]; got != "http://localhost:8080" {
		t.Errorf("env.values[NAVEXA_API_URL] = %q, want http://localhost:8080", got)
	}
	if m.Runtime.Run.Command != "go run ." {
		t.Errorf("runtime.run.command = %q, want unchanged", m.Runtime.Run.Command)
	}
}

func TestSetServiceEnvValueUpdatesInPlaceAndKeepsComments(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
env:
  # keep me
  values:
    K: old
    OTHER: untouched
`)

	if err := SetServiceEnvValue(ServiceManifestPath(dir), "K", "new"); err != nil {
		t.Fatalf("SetServiceEnvValue: %v", err)
	}

	m, err := LoadServiceManifest(dir)
	if err != nil {
		t.Fatalf("LoadServiceManifest: %v", err)
	}
	if got := m.Env.Values["K"]; got != "new" {
		t.Errorf("K = %q, want new", got)
	}
	if got := m.Env.Values["OTHER"]; got != "untouched" {
		t.Errorf("OTHER = %q, want untouched", got)
	}

	data, err := os.ReadFile(ServiceManifestPath(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "# keep me") {
		t.Errorf("comment was dropped:\n%s", data)
	}
}

// A bare "env:" parses as a null scalar; appending to it would emit garbage.
func TestSetServiceEnvValueOnBareEnvKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
env:
`)

	if err := SetServiceEnvValue(ServiceManifestPath(dir), "K", "v"); err != nil {
		t.Fatalf("SetServiceEnvValue: %v", err)
	}

	m, err := LoadServiceManifest(dir)
	if err != nil {
		t.Fatalf("LoadServiceManifest: %v", err)
	}
	if got := m.Env.Values["K"]; got != "v" {
		t.Errorf("K = %q, want v", got)
	}
}

func TestSetServiceEnvValueRequiresManifest(t *testing.T) {
	if err := SetServiceEnvValue(ServiceManifestPath(t.TempDir()), "K", "v"); err == nil {
		t.Fatal("expected an error when the service has no manifest")
	}
}
