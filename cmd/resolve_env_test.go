package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func writeManifest(t *testing.T, dir string) {
	t.Helper()
	body := `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
`
	if err := os.WriteFile(filepath.Join(dir, config.WorkspaceManifestFileName), []byte(body), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestResolveActiveEnvFromManifest(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	if err := config.SetEnvironment(dir, "staging", config.WorkspaceEnvironment{
		Type:          "remote",
		Observability: config.WorkspaceEnvironmentObservability{Backend: "signoz", URL: "https://signoz.staging", OTLPEndpoint: "https://otel.staging:4318"},
	}); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}

	ws := &workspace.Workspace{Name: "navexa", Path: dir}
	env, ok := resolveActiveEnv(ws, "staging")
	if !ok {
		t.Fatal("resolveActiveEnv did not find manifest env staging")
	}
	if env.Type != workspace.EnvironmentTypeRemote {
		t.Errorf("type = %q, want remote", env.Type)
	}
	if env.Observability.URL != "https://signoz.staging" {
		t.Errorf("url = %q, want https://signoz.staging", env.Observability.URL)
	}
	if env.Observability.OTLPEndpoint != "https://otel.staging:4318" {
		t.Errorf("otlpEndpoint = %q, want https://otel.staging:4318", env.Observability.OTLPEndpoint)
	}
}

func TestResolveActiveEnvSynthesizesLocalWhenNoneDefined(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)

	ws := &workspace.Workspace{Name: "navexa", Path: dir}
	env, ok := resolveActiveEnv(ws, "local")
	if !ok {
		t.Fatal("resolveActiveEnv did not synthesize local")
	}
	if env.Type != workspace.EnvironmentTypeLocal {
		t.Errorf("type = %q, want local", env.Type)
	}
	if env.Observability.Backend != "signoz" {
		t.Errorf("backend = %q, want signoz", env.Observability.Backend)
	}
}

func TestResolveActiveEnvMissingEnvErrors(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)

	ws := &workspace.Workspace{Name: "navexa", Path: dir}
	if _, ok := resolveActiveEnv(ws, "nope"); ok {
		t.Fatal("resolveActiveEnv found an undefined env")
	}
}

// allEnvironments must surface manifest-defined envs (the env add store) plus a
// synthesized local, so `env list` shows the same set env use/show operate on.
func TestAllEnvironmentsIncludesManifestAndLocal(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	if err := config.SetEnvironment(dir, "staging", config.WorkspaceEnvironment{Type: "remote"}); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}

	ws := &workspace.Workspace{Name: "navexa", Path: dir}
	all := allEnvironments(ws)
	if _, ok := all["staging"]; !ok {
		t.Error("allEnvironments missing manifest env staging")
	}
	if _, ok := all["local"]; !ok {
		t.Error("allEnvironments missing synthesized local")
	}
}
