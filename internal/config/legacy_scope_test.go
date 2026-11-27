package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const legacyManifest = `version: 1
workspace:
  name: other
  repoDiscovery:
    mode: explicit
    repos: [./api, ./db]
dependencies:
  api: [db]
`

func scopeLegacy(t *testing.T, dir string) {
	t.Helper()
	prev := legacyEdgeScope
	SetLegacyWarningScope(dir)
	t.Cleanup(func() { legacyEdgeScope = prev })
}

func TestGeneratedManifestsNeverWarn(t *testing.T) {
	buf := captureLegacyWarnings(t)
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, ReplicaDirName, "other"),
		filepath.Join(root, StacksDirName, "other", "feat"),
	} {
		mustWriteFile(t, filepath.Join(dir, WorkspaceManifestFileName), legacyManifest)
		if _, err := LoadWorkspaceManifest(dir); err != nil {
			t.Fatalf("load %s: %v", dir, err)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("a manifest devstack generated must not warn, got %q", buf.String())
	}
}

func TestLegacyWarningPrintsOnlyForTheTargetWorkspace(t *testing.T) {
	buf := captureLegacyWarnings(t)
	target := t.TempDir()
	other := t.TempDir()
	mustWriteFile(t, filepath.Join(target, WorkspaceManifestFileName), legacyManifest)
	mustWriteFile(t, filepath.Join(other, WorkspaceManifestFileName), legacyManifest)
	scopeLegacy(t, target)

	if _, err := LoadWorkspaceManifest(other); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("a workspace the command does not target must not warn, got %q", buf.String())
	}
	if _, err := LoadWorkspaceManifest(target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), filepath.Join(target, WorkspaceManifestFileName)) {
		t.Errorf("the target workspace must warn, got %q", buf.String())
	}
}
