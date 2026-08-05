package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSvc(t *testing.T, path, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	body := "version: 1\nservice:\n  name: " + name + "\nruntime:\n  run:\n    command: \"echo hi\"\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestInitWritesTheOriginalNameForTheFirstService(t *testing.T) {
	dir := t.TempDir()

	got, declared := initManifestTarget(dir, "api")
	if want := filepath.Join(dir, "devstack.service.yaml"); got != want {
		t.Errorf("target = %q, want %q", got, want)
	}
	if declared {
		t.Error("an empty directory declares no service")
	}
}

func TestInitNamesTheFileAfterEachServiceAfterTheFirst(t *testing.T) {
	dir := t.TempDir()
	writeSvc(t, filepath.Join(dir, "devstack.service.yaml"), "api")

	got, declared := initManifestTarget(dir, "worker")
	if want := filepath.Join(dir, "devstack.worker.yaml"); got != want {
		t.Errorf("target = %q, want %q", got, want)
	}
	if declared {
		t.Error("worker is not declared yet")
	}
}

func TestInitTargetsTheFileThatDeclaresTheNamedService(t *testing.T) {
	dir := t.TempDir()
	writeSvc(t, filepath.Join(dir, "devstack.service.yaml"), "api")
	writeSvc(t, filepath.Join(dir, "devstack.worker.yaml"), "worker")

	got, declared := initManifestTarget(dir, "worker")
	if want := filepath.Join(dir, "devstack.worker.yaml"); got != want {
		t.Errorf("target = %q, want the file that declares worker (%q)", got, want)
	}
	if !declared {
		t.Error("worker is declared, so init must refuse without --force")
	}

	got, declared = initManifestTarget(dir, "api")
	if want := filepath.Join(dir, "devstack.service.yaml"); got != want {
		t.Errorf("target = %q, want %q", got, want)
	}
	if !declared {
		t.Error("api is declared, so init must refuse without --force")
	}
}

func TestInitNeverWritesOverAManifestItCannotRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "devstack.service.yaml"), []byte("{{{ not yaml"), 0644); err != nil {
		t.Fatal(err)
	}

	got, declared := initManifestTarget(dir, "api")
	if want := filepath.Join(dir, "devstack.api.yaml"); got != want {
		t.Errorf("target = %q, want %q", got, want)
	}
	if declared {
		t.Error("an unreadable file declares nothing devstack can match")
	}
}
