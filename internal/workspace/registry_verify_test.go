package workspace

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestNextPortSkipsListeningPort(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	bound := ln.Addr().(*net.TCPAddr).Port

	if err := Save([]Workspace{{Name: "held", Path: tmpHome + "/dev/held", TiltPort: bound - 1}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	port, err := NextPort()
	if err != nil {
		t.Fatalf("NextPort(): %v", err)
	}
	if port == bound {
		t.Fatalf("NextPort() returned the bound port %d; the probe did not skip it", port)
	}
	if port <= bound {
		t.Fatalf("NextPort() = %d, want > %d (the bound port)", port, bound)
	}
}

func TestRegisterConcurrentDistinctPorts(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = Register(Workspace{
				Name: fmt.Sprintf("ws-%d", i),
				Path: fmt.Sprintf("%s/dev/ws-%d", tmpHome, i),
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Register ws-%d: %v", i, err)
		}
	}

	all, err := All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	if len(all) != n {
		t.Fatalf("got %d registrations, want %d (lost writes)", len(all), n)
	}
	seen := map[int]string{}
	for _, ws := range all {
		if prev, dup := seen[ws.TiltPort]; dup {
			t.Fatalf("duplicate TiltPort %d assigned to %q and %q", ws.TiltPort, prev, ws.Name)
		}
		seen[ws.TiltPort] = ws.Name
	}
}

func TestNextPortExhausted(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	orig := portInUse
	portInUse = func(int) bool { return true }
	defer func() { portInUse = orig }()

	if _, err := NextPort(); err == nil {
		t.Fatal("NextPort() with every port busy returned nil error, want an exhaustion error")
	}
}

func TestRegisterNameCollisionDifferentPath(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := Register(Workspace{Name: "navexa", Path: tmpHome + "/dev/navexa", TiltPort: 10350}); err != nil {
		t.Fatalf("Register base: %v", err)
	}

	err := Register(Workspace{Name: "navexa", Path: tmpHome + "/dev/other", TiltPort: 10351})
	if err == nil {
		t.Fatal("Register with colliding name at different path succeeded, want error")
	}
	if !strings.Contains(err.Error(), "navexa") {
		t.Fatalf("error %q does not name the conflicting workspace", err)
	}

	all, err := All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("registry mutated by rejected Register: got %d entries, want 1", len(all))
	}
	if all[0].Path != filepath.Clean(tmpHome+"/dev/navexa") {
		t.Fatalf("registry entry changed: %+v", all[0])
	}
}

func TestRegisterSameNameSamePathUpdates(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	path := tmpHome + "/dev/navexa"
	if err := Register(Workspace{Name: "navexa", Path: path, TiltPort: 10350}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := Register(Workspace{Name: "navexa", Path: path, TiltPort: 10360}); err != nil {
		t.Fatalf("Register same name same path: %v", err)
	}

	all, err := All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d entries, want 1", len(all))
	}
	if all[0].TiltPort != 10360 {
		t.Fatalf("in-place update failed: TiltPort = %d, want 10360", all[0].TiltPort)
	}
}

func TestRegisterDistinctNames(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := Register(Workspace{Name: "navexa", Path: tmpHome + "/dev/navexa", TiltPort: 10350}); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := Register(Workspace{Name: "other", Path: tmpHome + "/dev/other", TiltPort: 10351}); err != nil {
		t.Fatalf("Register b: %v", err)
	}

	all, err := All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d entries, want 2", len(all))
	}
}

func TestRegisterCaseInsensitiveCollision(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := Register(Workspace{Name: "Foo", Path: tmpHome + "/dev/foo", TiltPort: 10350}); err != nil {
		t.Fatalf("Register Foo: %v", err)
	}
	err := Register(Workspace{Name: "foo", Path: tmpHome + "/dev/foo2", TiltPort: 10351})
	if err == nil {
		t.Fatal("Register with case-insensitive colliding name succeeded, want error")
	}

	all, err := All()
	if err != nil {
		t.Fatalf("All(): %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("registry mutated by rejected Register: got %d entries, want 1", len(all))
	}
}

func TestDetectFromCwdLongestMatch(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	base := tmpHome + "/x/base"
	sibling := tmpHome + "/x/base-stack"
	nested := tmpHome + "/x/base/wt"
	for _, p := range []string{base, sibling, nested} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("MkdirAll %s: %v", p, err)
		}
	}

	baseWS := Workspace{Name: "base", Path: base, TiltPort: 10350}
	siblingWS := Workspace{Name: "base-stack", Path: sibling, TiltPort: 10351}
	nestedWS := Workspace{Name: "nested", Path: nested, TiltPort: 10352}

	orders := map[string][]Workspace{
		"base-first": {baseWS, siblingWS, nestedWS},
		"base-last":  {nestedWS, siblingWS, baseWS},
	}

	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	for label, order := range orders {
		t.Run(label, func(t *testing.T) {
			if err := Save(order); err != nil {
				t.Fatalf("Save: %v", err)
			}

			cwd := nested + "/deep/dir"
			if err := os.MkdirAll(cwd, 0755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.Chdir(cwd); err != nil {
				t.Fatalf("Chdir: %v", err)
			}
			detected, err := DetectFromCwd()
			if err != nil {
				t.Fatalf("DetectFromCwd(): %v", err)
			}
			if detected.Name != "nested" {
				t.Fatalf("DetectFromCwd() = %q, want %q (order %s)", detected.Name, "nested", label)
			}
		})
	}
}

// A legacy registry file that still carries the removed base_name field must
// load without error — a stack is no longer a workspace, but old files that
// recorded one as a workspace must not break the registry.
func TestLegacyBaseNameFieldStillLoads(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := os.MkdirAll(filepath.Dir(RegistryPath()), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `[
  {"name": "navexa--import", "path": "/home/x/dev/stack", "tilt_port": 10350, "base_name": "navexa"},
  {"name": "navexa", "path": "/home/x/dev/navexa", "tilt_port": 10351}
]`
	if err := os.WriteFile(RegistryPath(), []byte(legacy), 0644); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("got %d entries, want 2", len(loaded))
	}
	if loaded[0].Name != "navexa--import" || loaded[1].Name != "navexa" {
		t.Fatalf("unexpected names: %q, %q", loaded[0].Name, loaded[1].Name)
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
