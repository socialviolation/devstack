package hostdaemon

import (
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
	if err != nil || !strings.Contains(string(service), "ExecStart=/opt/bin/devstack tick\n") {
		t.Errorf("service unit = %q, %v", service, err)
	}
	timer, err := os.ReadFile(filepath.Join(dir, "devstack-sweep.timer"))
	if err != nil || !strings.Contains(string(timer), "OnUnitActiveSec=10min\n") || !strings.Contains(string(timer), "OnBootSec=2min\n") {
		t.Errorf("timer unit = %q, %v", timer, err)
	}

	*calls = nil
	installed, err = InstallSweepTimer("/opt/bin/devstack")
	if err != nil || installed || len(*calls) != 0 {
		t.Errorf("second install = %v, %v with calls %v; want a skip", installed, err, *calls)
	}

	installed, err = InstallSweepTimer("/usr/local/bin/devstack")
	if err != nil || !installed || len(*calls) != 2 {
		t.Errorf("install with a moved binary = %v, %v with calls %v; want a rewrite", installed, err, *calls)
	}
	if service, _ := os.ReadFile(filepath.Join(dir, "devstack-sweep.service")); !strings.Contains(string(service), "ExecStart=/usr/local/bin/devstack tick\n") {
		t.Errorf("service unit after the move = %q", service)
	}
}
