package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/workspace"
)

// One collector serves the whole machine, so 'devstack down' in one workspace
// must leave it up while another workspace still runs. devstack treats it like
// the daemon, and stops it only when no workspace is active.
func TestDownKeepsTheSharedCollectorWhileAnotherWorkspaceIsActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	alpha, beta := t.TempDir(), t.TempDir()
	for _, ws := range []workspace.Workspace{
		{Name: "alpha", Path: alpha, TiltPort: 10350},
		{Name: "beta", Path: beta, TiltPort: 10351},
	} {
		if err := workspace.Register(ws); err != nil {
			t.Fatalf("register %s: %v", ws.Name, err)
		}
	}
	if err := workspace.SetWorkspaceActive("beta", true); err != nil {
		t.Fatalf("activate beta: %v", err)
	}
	useWorkspaceKey(t, "alpha")

	var err error
	out := captureStdout(t, func() { err = runDown(&cobra.Command{Use: "down"}, nil) })
	if err != nil {
		t.Fatalf("runDown: %v", err)
	}

	if !strings.Contains(out, "still active") {
		t.Fatalf("down did not see beta as active, so this proves nothing: %s", out)
	}
	if strings.Contains(out, "OTEL") {
		t.Errorf("down reached the collector while beta was still active: %s", out)
	}
}

// With no workspace left active the daemon goes down, and the collector goes
// with it.
func TestDownStopsTheCollectorWhenNoWorkspaceIsLeft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	alpha := t.TempDir()
	if err := workspace.Register(workspace.Workspace{Name: "alpha", Path: alpha, TiltPort: 10350}); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	useWorkspaceKey(t, "alpha")

	var err error
	out := captureStdout(t, func() { err = runDown(&cobra.Command{Use: "down"}, nil) })
	if err != nil {
		t.Fatalf("runDown: %v", err)
	}

	if !strings.Contains(out, "OTEL") {
		t.Errorf("with nothing else active, down must act on the collector: %s", out)
	}
}
