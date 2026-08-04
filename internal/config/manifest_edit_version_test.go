package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const versionTestManifest = `# The workspace manifest of test-ws.
# devstack generates the Tiltfile from this file.
version: 1
workspace:
  name: test-ws
  # How devstack finds the services.
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
`

func versionWorkspace(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, WorkspaceManifestFileName), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The manifest is a file that a person reads. The version moves, and every
// comment around it stays.
func TestSetWorkspaceVersionKeepsTheComments(t *testing.T) {
	dir := versionWorkspace(t, versionTestManifest)

	if err := SetWorkspaceVersion(dir, 2, "v0.9.9 (abc1234)"); err != nil {
		t.Fatalf("SetWorkspaceVersion() = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, WorkspaceManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"# The workspace manifest of test-ws.",
		"# devstack generates the Tiltfile from this file.",
		"# How devstack finds the services.",
		"version: 2",
		"v0.9.9 (abc1234)",
		"- ./repos/api",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the manifest lost %q:\n%s", want, got)
		}
	}
	if v, err := WorkspaceVersion(dir); err != nil || v != 2 {
		t.Errorf("WorkspaceVersion() = %d, %v, want 2", v, err)
	}
}

// The note beside the version says who migrated, and it is written again each
// time. A file that collects one line for each run is a record, and the version
// is the whole of the state.
func TestSetWorkspaceVersionWritesOneNote(t *testing.T) {
	dir := versionWorkspace(t, versionTestManifest)

	if err := SetWorkspaceVersion(dir, 2, "v0.9.8 (aaaaaaa)"); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkspaceVersion(dir, 2, "v0.9.9 (bbbbbbb)"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, WorkspaceManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "aaaaaaa") {
		t.Errorf("the manifest keeps the note of an older run:\n%s", got)
	}
	if !strings.Contains(got, "bbbbbbb") {
		t.Errorf("the manifest never names the devstack that migrated:\n%s", got)
	}
}

// Version 1 is a workspace that needs a migration, and it must load. Refusing it
// would leave the user with a file that no devstack can read.
func TestVersionOneLoadsAndVersionTwoLoads(t *testing.T) {
	for _, v := range []string{"1", "2"} {
		dir := versionWorkspace(t, strings.Replace(versionTestManifest, "version: 1", "version: "+v, 1))
		if _, err := LoadWorkspaceManifest(dir); err != nil {
			t.Errorf("a manifest at version %s does not load: %v", v, err)
		}
	}
}

// A manifest that a newer devstack wrote is refused, and the message says both
// versions and what to do.
func TestAnUnknownFutureVersionIsRefused(t *testing.T) {
	dir := versionWorkspace(t, strings.Replace(versionTestManifest, "version: 1", "version: 3", 1))

	_, err := WorkspaceVersion(dir)
	if err == nil {
		t.Fatal("a manifest at an unknown version must be refused")
	}
	for _, want := range []string{"version 3", "version 2", "devstack upgrade"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message never states %q: %v", want, err)
		}
	}
}

// A directory that holds no workspace manifest holds no version, and that is
// not an error: nothing there can be migrated, and nothing can be stamped.
func TestADirectoryWithNoManifestHasNoVersion(t *testing.T) {
	v, err := WorkspaceVersion(t.TempDir())
	if err != nil || v != 0 {
		t.Errorf("WorkspaceVersion() = %d, %v, want 0 and no error", v, err)
	}
}
