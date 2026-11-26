package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/herdr"
)

func TestTickPublishesToHerdr(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	sock := filepath.Join(dir, "herdr.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", "/usr/bin/herdr-fake")
	t.Setenv("HERDR_SOCKET_PATH", sock)
	var calls []string
	prev := herdr.Run
	herdr.Run = func(bin string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(`{"result":{"panes":[]}}`), nil
	}
	t.Cleanup(func() { herdr.Run = prev })

	if err := runTick(nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "pane list" {
		t.Errorf("tick ran herdr %q, want one pane list", calls)
	}
}

func countDirectSweeps(t *testing.T, systemd bool) *int {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	n := 0
	prevHost, prevSweep := systemdHost, directSweep
	systemdHost = func() bool { return systemd }
	directSweep = func() { n++ }
	t.Cleanup(func() { systemdHost, directSweep = prevHost, prevSweep })
	return &n
}

func TestDirectModeSweepsAtMostOnceInTenMinutes(t *testing.T) {
	n := countDirectSweeps(t, false)
	start := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		at   time.Duration
		want int
	}{
		{0, 1},
		{time.Minute, 1},
		{9*time.Minute + 59*time.Second, 1},
		{10 * time.Minute, 2},
		{11 * time.Minute, 2},
		{25 * time.Minute, 3},
	} {
		sweepIfDirect(start.Add(c.at))
		if *n != c.want {
			t.Errorf("after a command at +%v: %d sweeps, want %d", c.at, *n, c.want)
		}
	}
}

func TestASystemdHostDoesNotSweepBeforeACommand(t *testing.T) {
	n := countDirectSweeps(t, true)
	start := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	sweepIfDirect(start)
	sweepIfDirect(start.Add(time.Hour))
	if *n != 0 {
		t.Errorf("a systemd host swept %d times before a command, want 0", *n)
	}
}

func TestCommandsThatMoveTheStopTimeDoNotSweepFirst(t *testing.T) {
	n := countDirectSweeps(t, false)
	for _, c := range []*cobra.Command{stackExtendCmd, stackUpCmd} {
		rootCmd.PersistentPreRun(c, nil)
		if *n != 0 {
			t.Fatalf("%s swept %d times before it ran, want 0", c.CommandPath(), *n)
		}
	}
	rootCmd.PersistentPreRun(stackListCmd, nil)
	if *n != 1 {
		t.Errorf("stack list swept %d times before it ran, want 1", *n)
	}
}

func TestStopAfterReadsTheLegacyTTLWithAWarning(t *testing.T) {
	for _, c := range []struct {
		name, stopAfter, ttl, maxStacks string
		want                            time.Duration
		warn                            []string
	}{
		{name: "default", want: 24 * time.Hour},
		{name: "new name", stopAfter: "2m", want: 2 * time.Minute},
		{name: "legacy ttl", ttl: "6h", want: 6 * time.Hour, warn: []string{"DEVSTACK_AGENT_TTL"}},
		{name: "new name wins", stopAfter: "3h", ttl: "6h", want: 3 * time.Hour},
		{name: "quota ignored", maxStacks: "9", want: 24 * time.Hour, warn: []string{"no agent quota"}},
		{name: "bad value", stopAfter: "soon", want: 24 * time.Hour, warn: []string{`"soon"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("DEVSTACK_STOP_AFTER", c.stopAfter)
			t.Setenv("DEVSTACK_AGENT_TTL", c.ttl)
			t.Setenv("DEVSTACK_AGENT_MAX_STACKS", c.maxStacks)
			v := viper.New()
			v.BindEnv("stop_after", "DEVSTACK_STOP_AFTER")
			v.BindEnv("agents.max_stacks", "DEVSTACK_AGENT_MAX_STACKS")
			v.BindEnv("agents.ttl", "DEVSTACK_AGENT_TTL")

			var warn bytes.Buffer
			if got := stopAfter(v, &warn); got != c.want {
				t.Errorf("stopAfter = %v, want %v", got, c.want)
			}
			for _, w := range c.warn {
				if !strings.Contains(warn.String(), w) {
					t.Errorf("warning %q lacks %q", warn.String(), w)
				}
			}
			if len(c.warn) == 0 && warn.Len() > 0 {
				t.Errorf("unexpected warning %q", warn.String())
			}
		})
	}
}
