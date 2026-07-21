package workspace

import "testing"

func TestSetWorkspaceActiveRoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := Register(Workspace{Name: "navexa", Path: tmpHome + "/dev/navexa", TiltPort: 10350}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	found, err := FindByName("navexa")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	if found.Active {
		t.Fatalf("new workspace should be inactive, got Active=true")
	}

	if err := SetWorkspaceActive("navexa", true); err != nil {
		t.Fatalf("SetWorkspaceActive(true): %v", err)
	}
	found, err = FindByName("navexa")
	if err != nil {
		t.Fatalf("FindByName after activate: %v", err)
	}
	if !found.Active {
		t.Fatalf("workspace not persisted as active")
	}

	if err := SetWorkspaceActive("navexa", false); err != nil {
		t.Fatalf("SetWorkspaceActive(false): %v", err)
	}
	found, err = FindByName("navexa")
	if err != nil {
		t.Fatalf("FindByName after deactivate: %v", err)
	}
	if found.Active {
		t.Fatalf("workspace not persisted as inactive")
	}
}

func TestSetWorkspaceActiveUnknown(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := SetWorkspaceActive("ghost", true); err == nil {
		t.Fatal("SetWorkspaceActive on unknown workspace succeeded, want error")
	}
}

func TestActiveWorkspacesFiltering(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := Register(Workspace{Name: "a", Path: tmpHome + "/dev/a", TiltPort: 10350}); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := Register(Workspace{Name: "b", Path: tmpHome + "/dev/b", TiltPort: 10351}); err != nil {
		t.Fatalf("Register b: %v", err)
	}
	if err := Register(Workspace{Name: "c", Path: tmpHome + "/dev/c", TiltPort: 10352}); err != nil {
		t.Fatalf("Register c: %v", err)
	}

	if err := SetWorkspaceActive("a", true); err != nil {
		t.Fatalf("activate a: %v", err)
	}
	if err := SetWorkspaceActive("c", true); err != nil {
		t.Fatalf("activate c: %v", err)
	}

	active, err := ActiveWorkspaces()
	if err != nil {
		t.Fatalf("ActiveWorkspaces: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("ActiveWorkspaces returned %d, want 2", len(active))
	}
	got := map[string]bool{}
	for _, ws := range active {
		got[ws.Name] = true
	}
	if !got["a"] || !got["c"] || got["b"] {
		t.Fatalf("ActiveWorkspaces returned wrong set: %+v", got)
	}
}

func TestAnyWorkspaceActive(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	any, err := AnyWorkspaceActive()
	if err != nil {
		t.Fatalf("AnyWorkspaceActive (empty): %v", err)
	}
	if any {
		t.Fatal("AnyWorkspaceActive on empty registry = true, want false")
	}

	if err := Register(Workspace{Name: "a", Path: tmpHome + "/dev/a", TiltPort: 10350}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	any, err = AnyWorkspaceActive()
	if err != nil {
		t.Fatalf("AnyWorkspaceActive (inactive): %v", err)
	}
	if any {
		t.Fatal("AnyWorkspaceActive with only inactive workspaces = true, want false")
	}

	if err := SetWorkspaceActive("a", true); err != nil {
		t.Fatalf("activate: %v", err)
	}
	any, err = AnyWorkspaceActive()
	if err != nil {
		t.Fatalf("AnyWorkspaceActive (active): %v", err)
	}
	if !any {
		t.Fatal("AnyWorkspaceActive with an active workspace = false, want true")
	}
}
