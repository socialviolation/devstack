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

func TestSetEnvironmentRoundTrips(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceManifest(t, dir)

	env := WorkspaceEnvironment{
		Type: "remote",
		Observability: WorkspaceEnvironmentObservability{
			Backend:      "signoz",
			URL:          "https://signoz.staging",
			OTLPEndpoint: "https://otel.staging:4318",
		},
	}
	if err := SetEnvironment(dir, "staging", env); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	got := m.Environments["staging"]
	if got.Type != "remote" {
		t.Errorf("type = %q, want remote", got.Type)
	}
	if got.Observability.URL != "https://signoz.staging" {
		t.Errorf("url = %q, want https://signoz.staging", got.Observability.URL)
	}
	if got.Observability.OTLPEndpoint != "https://otel.staging:4318" {
		t.Errorf("otlpEndpoint = %q, want https://otel.staging:4318", got.Observability.OTLPEndpoint)
	}
}

func TestSetEnvironmentPreservesValues(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceManifest(t, dir)

	if err := SetEnvValue(dir, "staging", "API_URL", "http://staging"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}
	if err := SetEnvironment(dir, "staging", WorkspaceEnvironment{Type: "remote"}); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	if got := m.Environments["staging"].Values["API_URL"]; got != "http://staging" {
		t.Errorf("values.API_URL = %q, want http://staging (env add must not drop values)", got)
	}
	if got := m.Environments["staging"].Type; got != "remote" {
		t.Errorf("type = %q, want remote", got)
	}
}

func TestSetEnvironmentClearsEmptyObservability(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceManifest(t, dir)

	full := WorkspaceEnvironment{Observability: WorkspaceEnvironmentObservability{URL: "http://x", OTLPEndpoint: "http://y"}}
	if err := SetEnvironment(dir, "staging", full); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}
	if err := SetEnvironment(dir, "staging", WorkspaceEnvironment{Type: "local"}); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	if got := m.Environments["staging"].Observability.URL; got != "" {
		t.Errorf("url = %q, want cleared", got)
	}
}

func TestRemoveEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceManifest(t, dir)

	if err := SetEnvironment(dir, "staging", WorkspaceEnvironment{Type: "remote"}); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
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

// The bug this change fixes: `env add` (SetEnvironment) and `env use` (which
// checks m.Environments[name]) must share one store, so an added env is
// immediately usable — no "not defined in workspace environments" error.
func TestEnvAddThenUseSameStore(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceManifest(t, dir)

	if err := SetEnvironment(dir, "staging", WorkspaceEnvironment{
		Type:          "remote",
		Observability: WorkspaceEnvironmentObservability{URL: "https://signoz.staging"},
	}); err != nil {
		t.Fatalf("env add path: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	if _, ok := m.Environments["staging"]; !ok {
		t.Fatal("env use path could not find staging — stores are not unified")
	}
}
