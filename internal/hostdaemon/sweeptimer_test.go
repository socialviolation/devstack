package hostdaemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeSystemctl(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	prev := systemctlUser
	systemctlUser = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { systemctlUser = prev })
	return &calls
}

func TestInstallSweepTimerWritesBothUnitsOnce(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("PATH", "/home/u/.local/bin:/usr/bin")
	calls := fakeSystemctl(t)

	installed, err := InstallSweepTimer("/opt/bin/devstack")
	if err != nil || !installed {
		t.Fatalf("first install = %v, %v; want installed", installed, err)
	}
	if got := strings.Join(*calls, "; "); got != "daemon-reload; enable --now devstack-sweep.timer" {
		t.Errorf("systemctl calls = %q", got)
	}
	dir := filepath.Join(cfg, "systemd", "user")
	service, err := os.ReadFile(filepath.Join(dir, "devstack-sweep.service"))
	if err != nil || !strings.Contains(string(service), "ExecStart=/opt/bin/devstack tick\n") || !strings.Contains(string(service), "Environment=PATH=/home/u/.local/bin:/usr/bin\n") {
		t.Errorf("service unit = %q, %v", service, err)
	}
	timer, err := os.ReadFile(filepath.Join(dir, "devstack-sweep.timer"))
	if err != nil || !strings.Contains(string(timer), "OnUnitActiveSec=10min\n") || !strings.Contains(string(timer), "OnBootSec=2min\n") {
		t.Errorf("timer unit = %q, %v", timer, err)
	}

	*calls = nil
	installed, err = InstallSweepTimer("/opt/bin/devstack")
	if got := strings.Join(*calls, "; "); err != nil || installed || got != "is-enabled devstack-sweep.timer; is-active devstack-sweep.timer" {
		t.Errorf("second install = %v, %v with calls %q; want a check and a skip", installed, err, got)
	}
	*calls = nil

	installed, err = InstallSweepTimer("/usr/local/bin/devstack")
	if err != nil || !installed || len(*calls) != 2 {
		t.Errorf("install with a moved binary = %v, %v with calls %v; want a rewrite", installed, err, *calls)
	}
	if service, _ := os.ReadFile(filepath.Join(dir, "devstack-sweep.service")); !strings.Contains(string(service), "ExecStart=/usr/local/bin/devstack tick\n") {
		t.Errorf("service unit after the move = %q", service)
	}
}

func TestInstallSweepTimerEnablesATimerThatIsNotEnabled(t *testing.T) {
	for _, check := range []string{"is-enabled", "is-active"} {
		t.Run(check, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			calls := fakeSystemctl(t)
			if _, err := InstallSweepTimer("/opt/bin/devstack"); err != nil {
				t.Fatal(err)
			}
			*calls = nil
			ok := systemctlUser
			systemctlUser = func(args ...string) error {
				_ = ok(args...)
				if args[0] == check {
					return errors.New("disabled")
				}
				return nil
			}

			installed, err := InstallSweepTimer("/opt/bin/devstack")
			if err != nil || !installed {
				t.Errorf("install = %v, %v; want installed", installed, err)
			}
			if got := strings.Join(*calls, "; "); !strings.HasSuffix(got, "daemon-reload; enable --now devstack-sweep.timer") {
				t.Errorf("systemctl calls = %q, want a reload and an enable", got)
			}
		})
	}
}
