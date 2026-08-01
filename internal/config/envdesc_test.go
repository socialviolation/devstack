package config

import (
	"path/filepath"
	"testing"
)

// An environment name says nothing about what selecting it does. "fx-prod"
// reads as another local variant until the description says it points at
// production, which is the fact that changes what you do.
func TestEnvironmentDescriptionParses(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos: [./api]
environments:
  fx-prod:
    description: Exchange Rate PoC against the PRODUCTION database.
    values:
      DB: prod
  dev:
    values:
      DB: local
`)
	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest(): %v", err)
	}
	if got := m.Environments["fx-prod"].Description; got != "Exchange Rate PoC against the PRODUCTION database." {
		t.Errorf("description = %q", got)
	}
	if got := m.Environments["fx-prod"].Values["DB"]; got != "prod" {
		t.Errorf("values should still parse alongside the description, got %q", got)
	}
	if got := m.Environments["dev"].Description; got != "" {
		t.Errorf("an environment without a description = %q, want empty", got)
	}
}
