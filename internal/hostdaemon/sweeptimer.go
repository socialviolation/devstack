package hostdaemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const SweepUnit = "devstack-sweep"

var systemctlUser = func(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sweepUnits(bin string) map[string]string {
	return map[string]string{
		SweepUnit + ".service": "[Unit]\n" +
			"Description=devstack stops the stacks that are past their stop time\n\n" +
			"[Service]\n" +
			"Type=oneshot\n" +
			"ExecStart=" + bin + " tick\n",
		SweepUnit + ".timer": "[Unit]\n" +
			"Description=Run devstack tick every 10 minutes\n\n" +
			"[Timer]\n" +
			"OnBootSec=2min\n" +
			"OnUnitActiveSec=10min\n\n" +
			"[Install]\n" +
			"WantedBy=timers.target\n",
	}
}

func InstallSweepTimer(bin string) (installed bool, err error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return false, fmt.Errorf("can not find the user configuration directory: %w", err)
	}
	dir := filepath.Join(cfg, "systemd", "user")
	units := sweepUnits(bin)
	same := true
	for name, want := range units {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			same = false
		}
	}
	if same {
		if systemctlUser("is-enabled", SweepUnit+".timer") == nil && systemctlUser("is-active", SweepUnit+".timer") == nil {
			return false, nil
		}
		if err := systemctlUser("daemon-reload"); err != nil {
			return false, err
		}
		if err := systemctlUser("enable", "--now", SweepUnit+".timer"); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("can not create %s: %w", dir, err)
	}
	for name, content := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			return false, fmt.Errorf("can not write %s: %w", name, err)
		}
	}
	if err := systemctlUser("daemon-reload"); err != nil {
		return false, err
	}
	if err := systemctlUser("enable", "--now", SweepUnit+".timer"); err != nil {
		return false, err
	}
	return true, nil
}
