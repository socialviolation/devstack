package replica

import (
	"path/filepath"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func TestResolveInheritsObservabilityAndRuntime(t *testing.T) {
	ws := newInstrumentedTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	rw, err := Resolve(ws)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !rw.Manifest.Observability.IsEnabled() {
		t.Errorf("Observability = %+v, want the template's enabled block", rw.Manifest.Observability)
	}
	if got := rw.Manifest.Observability.Backend; got != "openobserve" {
		t.Errorf("Observability.Backend = %q, want openobserve", got)
	}
	if got := rw.Manifest.Runtime.Infra.Provider; got != "compose" {
		t.Errorf("Runtime.Infra.Provider = %q, want compose", got)
	}
	if got := rw.Manifest.Runtime.Infra.ComposeFiles; len(got) != 1 || got[0] != "./docker-compose.yml" {
		t.Errorf("Runtime.Infra.ComposeFiles = %v, want the template's file list", got)
	}
}

func newInstrumentedTemplate(t *testing.T) *workspace.Workspace {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	makeRepo(t, filepath.Join(root, "backend"), backendSvc)
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  env: dev
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
runtime:
  orchestrator: tilt
  infra:
    provider: compose
    composeFiles:
      - ./docker-compose.yml
observability:
  enabled: true
  backend: openobserve
`)
	return &workspace.Workspace{Name: "navexa", Path: root}
}
