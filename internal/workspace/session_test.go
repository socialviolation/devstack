package workspace

import (
	"net"
	"path/filepath"
	"testing"
)

func TestSessionLifecycleAndResidueDetection(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	ws := &Workspace{Name: "playground", Path: filepath.Join(tmpHome, "playground"), TiltPort: 12345}
	state, err := OpenSession(ws, 999999, []int{12345})
	if err != nil {
		t.Fatalf("OpenSession(): %v", err)
	}
	if state.Status != "open" {
		t.Fatalf("state.Status = %q", state.Status)
	}
	loaded, err := LoadSession(ws.Name)
	if err != nil {
		t.Fatalf("LoadSession(): %v", err)
	}
	if loaded.WorkspaceName != ws.Name {
		t.Fatalf("loaded.WorkspaceName = %q", loaded.WorkspaceName)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(): %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	residue := DetectResidue(0, []int{port})
	if len(residue) != 1 {
		t.Fatalf("DetectResidue() = %#v, want 1 residue item", residue)
	}

	if err := CloseSession(ws.Name, residue); err != nil {
		t.Fatalf("CloseSession(): %v", err)
	}
	closed, err := LoadSession(ws.Name)
	if err != nil {
		t.Fatalf("LoadSession(closed): %v", err)
	}
	if closed.Status != "unclean" {
		t.Fatalf("closed.Status = %q, want unclean", closed.Status)
	}
}
