package config

import (
	"path/filepath"
	"testing"
)

func writeWorkspaceManifest(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
`)
}

func TestRemoveEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceManifest(t, dir)

	if err := SetEnvValue(dir, "staging", "API_URL", "http://staging"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}
	if err := RemoveEnvironment(dir, "staging"); err != nil {
		t.Fatalf("RemoveEnvironment: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	if _, ok := m.Environments["staging"]; ok {
		t.Errorf("staging still present after RemoveEnvironment")
	}
}

// Manifests written before environments became pure config patches still carry
// type/observability keys under an environment. They must parse and degrade to a
// plain value patch rather than failing the whole workspace load.
func TestLegacyEnvironmentKeysAreInert(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
environments:
  remoteprod:
    type: remote
    observability:
      backend: signoz
      url: https://signoz.example
      otlpEndpoint: https://otel.example:4318
      apiKey: not-a-real-key
    values:
      API_URL: https://api.example
`)

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest with legacy environment keys: %v", err)
	}
	env, ok := m.Environments["remoteprod"]
	if !ok {
		t.Fatal("environment remoteprod not parsed")
	}
	if got := env.Values["API_URL"]; got != "https://api.example" {
		t.Errorf("values.API_URL = %q, want https://api.example", got)
	}
	if len(env.Values) != 1 {
		t.Errorf("values = %v, want only API_URL — legacy keys must not leak in", env.Values)
	}
}

// SetEnvValue must keep working on an environment that still carries legacy
// keys: the patch is edited in place and the inert keys are left alone.
func TestSetEnvValueOnLegacyEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
environments:
  staging:
    type: remote
    values:
      API_URL: https://old
`)

	if err := SetEnvValue(dir, "staging", "API_URL", "https://new"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	if got := m.Environments["staging"].Values["API_URL"]; got != "https://new" {
		t.Errorf("values.API_URL = %q, want https://new", got)
	}
}
