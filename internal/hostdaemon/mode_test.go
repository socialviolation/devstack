package hostdaemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func withHostDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(workspace.HostTiltDir(), 0755); err != nil {
		t.Fatal(err)
	}
}

func writePID(t *testing.T, pid int) {
	t.Helper()
	if err := os.WriteFile(workspace.HostPIDFile(), []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}
}

func stubSession(t *testing.T, live bool) *int {
	t.Helper()
	calls := 0
	prev := systemdUserSession
	systemdUserSession = func() bool { calls++; return live }
	t.Cleanup(func() { systemdUserSession = prev })
	return &calls
}

func TestDaemonModeReadsTheRecordOfARunningDaemon(t *testing.T) {
	for _, mode := range []string{ModeSystemd, ModeDirect} {
		withHostDir(t)
		writePID(t, os.Getpid())
		if err := writeMode(mode); err != nil {
			t.Fatalf("writeMode: %v", err)
		}
		if got := DaemonMode(); got != mode {
			t.Errorf("DaemonMode() = %q, want %q", got, mode)
		}
	}
}

func TestDaemonModeWithoutARecordIsDirectForAPreUpgradeDaemon(t *testing.T) {
	withHostDir(t)
	writePID(t, os.Getpid())
	if got := DaemonMode(); got != ModeDirect {
		t.Errorf("DaemonMode() = %q, want %q", got, ModeDirect)
	}
}

func TestDaemonModeIgnoresTheRecordWhenNoDaemonRuns(t *testing.T) {
	withHostDir(t)
	if err := writeMode(ModeDirect); err != nil {
		t.Fatal(err)
	}
	if got := DaemonMode(); got != "" {
		t.Errorf("DaemonMode() with no pid file = %q, want empty", got)
	}
}

func TestRenderScopesFollowTheRecordedModeNotTheLiveCheck(t *testing.T) {
	for _, tc := range []struct {
		mode string
		live bool
		want bool
	}{
		{ModeSystemd, false, true},
		{ModeDirect, true, false},
	} {
		withHostDir(t)
		calls := stubSession(t, tc.live)
		writePID(t, os.Getpid())
		if err := writeMode(tc.mode); err != nil {
			t.Fatal(err)
		}
		if got := renderScopes(); got != tc.want {
			t.Errorf("mode %s: renderScopes() = %v, want %v", tc.mode, got, tc.want)
		}
		if *calls != 0 {
			t.Errorf("mode %s: render checked the live session %d time(s)", tc.mode, *calls)
		}
	}
}

func TestRenderScopesWithNoDaemonUseTheSessionCheck(t *testing.T) {
	withHostDir(t)
	calls := stubSession(t, true)
	if !renderScopes() {
		t.Errorf("renderScopes() = false, want the session check's true")
	}
	if *calls != 1 {
		t.Errorf("session check calls = %d, want 1", *calls)
	}
}

func TestSyncWritesTheTiltfileOwnerOnly(t *testing.T) {
	withHostDir(t)
	stubSession(t, false)
	path := filepath.Join(workspace.HostTiltDir(), "Tiltfile")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Tiltfile mode = %o, want 600", info.Mode().Perm())
	}
}
