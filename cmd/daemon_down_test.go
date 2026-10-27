package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func recordDeadDaemon(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(workspace.HostModeFile()), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace.HostModeFile(), []byte(mode), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace.HostPIDFile(), []byte("2147483646"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDeadSystemdDaemonNamesTheUnitResult(t *testing.T) {
	recordDeadDaemon(t, hostdaemon.ModeSystemd)
	var asked []string
	prev := systemctlUser
	systemctlUser = func(args ...string) ([]byte, error) {
		asked = args
		return []byte("ActiveState=failed\nResult=oom-kill\nExecMainStatus=9\nInactiveEnterTimestamp=Tue 2026-10-27 14:15:58 AEDT\n"), nil
	}
	t.Cleanup(func() { systemctlUser = prev })

	got := hostDaemonDown()
	for _, want := range []string{
		"The host daemon is down: Result=oom-kill ExecMainStatus=9 at Tue 2026-10-27 14:15:58 AEDT",
		"Logs: journalctl --user -u devstack-tilt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hostDaemonDown() = %q, want %q", got, want)
		}
	}
	if strings.Join(asked, " ") != "show devstack-tilt -p ActiveState,Result,ExecMainStatus,InactiveEnterTimestamp" {
		t.Errorf("systemctl asked %q", asked)
	}
}

func TestDeadDirectDaemonPointsAtTheLog(t *testing.T) {
	recordDeadDaemon(t, hostdaemon.ModeDirect)
	got := hostDaemonDown()
	for _, want := range []string{"The host daemon is down: its pid 2147483646 is gone", workspace.HostLogFile()} {
		if !strings.Contains(got, want) {
			t.Errorf("hostDaemonDown() = %q, want %q", got, want)
		}
	}
}

func TestStackStatusSaysTheDaemonIsDownNotTheStack(t *testing.T) {
	recordDeadDaemon(t, hostdaemon.ModeDirect)
	rec := &stack.Record{Name: "feat", Base: "acme", Active: true}
	out := captureStdout(t, func() {
		if err := runStackStatus(&workspace.Workspace{Name: "acme"}, rec); err != nil {
			t.Fatalf("runStackStatus: %v", err)
		}
	})
	if !strings.Contains(out, "The host daemon is down") {
		t.Errorf("stack status = %q, want it to say the host daemon is down", out)
	}
	if strings.Contains(out, "is not up") {
		t.Errorf("stack status = %q, want no claim that the stack is not up", out)
	}
}
