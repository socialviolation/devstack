package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, WorkspaceManifestFileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readManifest(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, WorkspaceManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEditKeepsEveryDocument(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `version: 1
workspace:
  name: test-ws
  repoDiscovery:
    mode: scan
    roots: [.]
---
other: doc
---
third: doc
`)

	if err := SetWorkspaceVersion(dir, 2, "v9.9.9"); err != nil {
		t.Fatalf("SetWorkspaceVersion: %v", err)
	}

	out := readManifest(t, dir)
	if !strings.Contains(out, "other: doc") {
		t.Fatalf("the second document is gone:\n%s", out)
	}
	if !strings.Contains(out, "third: doc") {
		t.Fatalf("the third document is gone:\n%s", out)
	}
	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest after the edit: %v", err)
	}
	if m.Version != 2 {
		t.Fatalf("version = %d, want 2", m.Version)
	}
}

func TestEditKeepsTheLeadingDocumentMarker(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `---
version: 1
workspace:
  name: test-ws
  repoDiscovery:
    mode: scan
    roots: [.]
`)

	if err := SetWorkspaceVersion(dir, 2, "v9.9.9"); err != nil {
		t.Fatalf("SetWorkspaceVersion: %v", err)
	}

	out := readManifest(t, dir)
	if !strings.HasPrefix(out, "---\n") {
		t.Fatalf("the leading document marker is gone:\n%s", out)
	}
	if _, err := LoadWorkspaceManifest(dir); err != nil {
		t.Fatalf("LoadWorkspaceManifest after the edit: %v", err)
	}
}

func TestSetObservabilityEnabledReplacesAQuotedBool(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `version: 1
workspace:
  name: test-ws
  repoDiscovery:
    mode: scan
    roots: [.]
observability:
  enabled: "false"
`)

	if err := SetObservabilityEnabled(dir, true); err != nil {
		t.Fatalf("SetObservabilityEnabled: %v", err)
	}

	out := readManifest(t, dir)
	if strings.Contains(out, `"true"`) {
		t.Fatalf("enabled is still quoted, so it is a string and not a bool:\n%s", out)
	}
	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest after the edit: %v", err)
	}
	if !m.Observability.IsEnabled() {
		t.Fatal("observability.enabled did not survive the round trip")
	}
}

func TestSetEnvValueKeepsAQuotedStringQuotedWhereItMatters(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `version: 1
workspace:
  name: test-ws
  repoDiscovery:
    mode: scan
    roots: [.]
environments:
  dev:
    values:
      PORT: "8080"
`)

	if err := SetEnvValue(dir, "dev", "PORT", "9090"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest after the edit: %v", err)
	}
	if got := m.Environments["dev"].Values["PORT"]; got != "9090" {
		t.Fatalf("PORT = %q, want %q", got, "9090")
	}
}
