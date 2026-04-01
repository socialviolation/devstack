package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryFunctions(t *testing.T) {
	// Set up a temporary home directory
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Also patch UserHomeDir by overriding HOME (os.UserHomeDir reads HOME on Linux)
	// Verify RegistryPath uses the temp home
	expectedRegistry := filepath.Join(tmpHome, ".config", "devstack", "workspaces.json")
	if got := RegistryPath(); got != expectedRegistry {
		t.Fatalf("RegistryPath() = %q, want %q", got, expectedRegistry)
	}

	// Register first workspace
	ws1 := Workspace{
		Name:     "navexa",
		Path:     tmpHome + "/dev/navexa",
		TiltPort: 10350,
	}
	if err := Register(ws1); err != nil {
		t.Fatalf("Register(ws1): %v", err)
	}

	// Register second workspace with auto-assigned port
	ws2 := Workspace{
		Name: "otherproject",
		Path: tmpHome + "/dev/other",
		// TiltPort 0 — should auto-assign 10351
	}
	if err := Register(ws2); err != nil {
		t.Fatalf("Register(ws2): %v", err)
	}

	// All() should return 2 entries
	all, err := All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("All() returned %d workspaces, want 2", len(all))
	}

	// FindByName — found
	found, err := FindByName("navexa")
	if err != nil {
		t.Fatalf("FindByName(navexa): %v", err)
	}
	if found.Name != "navexa" {
		t.Fatalf("FindByName returned wrong workspace: %+v", found)
	}

	// FindByName — case-insensitive
	found2, err := FindByName("NAVEXA")
	if err != nil {
		t.Fatalf("FindByName(NAVEXA): %v", err)
	}
	if found2.Name != "navexa" {
		t.Fatalf("FindByName case-insensitive returned wrong workspace: %+v", found2)
	}

	// DetectFromCwd from a subdir of ws1
	subdir := ws1.Path + "/some/subdir"
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(subdir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	detected, err := DetectFromCwd()
	if err != nil {
		t.Fatalf("DetectFromCwd(): %v", err)
	}
	if detected.Name != "navexa" {
		t.Fatalf("DetectFromCwd() returned %q, want %q", detected.Name, "navexa")
	}

	// NextPort should return 10352 (max is 10351 from auto-assigned ws2, +1)
	port, err := NextPort()
	if err != nil {
		t.Fatalf("NextPort(): %v", err)
	}
	if port != 10352 {
		t.Fatalf("NextPort() = %d, want 10352", port)
	}
}

func TestNextPortEmpty(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	port, err := NextPort()
	if err != nil {
		t.Fatalf("NextPort() on empty registry: %v", err)
	}
	if port != 10350 {
		t.Fatalf("NextPort() on empty registry = %d, want 10350", port)
	}
}

func TestRegisterIdempotent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	ws := Workspace{Name: "first", Path: tmpHome + "/dev/first", TiltPort: 10350}
	if err := Register(ws); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Register again with same path but different name
	ws.Name = "updated"
	if err := Register(ws); err != nil {
		t.Fatalf("Register (update): %v", err)
	}

	all, err := All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("After idempotent Register, got %d workspaces, want 1", len(all))
	}
	if all[0].Name != "updated" {
		t.Fatalf("Idempotent Register didn't update name: got %q", all[0].Name)
	}
}
