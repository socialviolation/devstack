package otel

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/socialviolation/devstack/internal/workspace"
)

// hostDataDir is where the one collector's PID file and log live.
func hostDataDir() string {
	return workspace.DataDir(workspace.HostWorkspace().Name)
}

func collectorPIDFile() string {
	return filepath.Join(hostDataDir(), "collector.pid")
}

// CollectorConfigPath returns the path of the host collector's generated config.
func CollectorConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("devstack can not determine the home directory: %w", err)
	}
	return filepath.Join(home, ".config", "devstack", "collector", "config.yaml"), nil
}

// otelcolBin returns the path to the otelcol-contrib binary.
// Checks OTELCOL_BIN env var first, then PATH.
func otelcolBin() (string, error) {
	if bin := os.Getenv("OTELCOL_BIN"); bin != "" {
		return bin, nil
	}
	path, err := exec.LookPath("otelcol-contrib")
	if err != nil {
		return "", fmt.Errorf(`devstack can not find otelcol-contrib. Install it:
  macOS:  brew install opentelemetry-collector-contrib
  Linux:  https://github.com/open-telemetry/opentelemetry-collector-releases/releases
Or set OTELCOL_BIN=/path/to/binary`)
	}
	return path, nil
}

// StartCollector writes the merged config for every contributing workspace and
// (re)starts the one host collector so the new config takes effect. Restarting
// is what lets a second workspace coming up be folded into the running collector.
func StartCollector(contribs []WorkspaceContribution) error {
	bin, err := otelcolBin()
	if err != nil {
		return err
	}

	cfg, err := BuildConfig(workspace.OTLPGRPCPort, workspace.OTLPHTTPPort, contribs)
	if err != nil {
		return err
	}

	cfgPath, err := CollectorConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return fmt.Errorf("can not create the collector config directory: %w", err)
	}
	if err := os.WriteFile(cfgPath, cfg, 0600); err != nil {
		return fmt.Errorf("can not write the collector configuration: %w", err)
	}
	// WriteFile leaves an existing file's mode alone, and the generated config
	// embeds backend credentials.
	if err := os.Chmod(cfgPath, 0600); err != nil {
		return fmt.Errorf("can not secure the collector configuration: %w", err)
	}

	stopLegacyCollectors()

	if CollectorRunning() {
		if err := StopCollector(); err != nil {
			return err
		}
	}
	if err := awaitPortFree(workspace.OTLPGRPCPort); err != nil {
		return err
	}

	pidPath := collectorPIDFile()
	if err := os.MkdirAll(filepath.Dir(pidPath), 0755); err != nil {
		return fmt.Errorf("can not create the data directory: %w", err)
	}

	// Detached background process, logging to a file — never inherit the caller's
	// stdout/stderr, which would keep its pipe open and block the caller.
	logFile, err := os.OpenFile(filepath.Join(filepath.Dir(pidPath), "collector.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("can not open the collector log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin, "--config="+cfgPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("can not start otelcol-contrib: %w", err)
	}

	pid := cmd.Process.Pid
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		// Don't fail — process is running, just can't track it
		fmt.Fprintf(os.Stderr, "warning: devstack can not write the collector PID file: %v\n", err)
	}

	return nil
}

// stopLegacyCollectors kills collectors left over from when devstack ran one per
// workspace. They hold the OTLP and telemetry ports the host collector needs, so
// without this a machine that ran an older devstack can never start the new one.
func stopLegacyCollectors() {
	entries, err := os.ReadDir(workspace.DataRoot())
	if err != nil {
		return
	}
	hostKey := workspace.HostWorkspace().Name
	for _, e := range entries {
		if !e.IsDir() || e.Name() == hostKey {
			continue
		}
		pidPath := filepath.Join(workspace.DataRoot(), e.Name(), "collector.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && isCollectorProcess(pid) {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Signal(os.Interrupt)
			}
		}
		os.Remove(pidPath)
	}
}

// isCollectorProcess reports whether a PID is actually an otelcol. A leftover
// PID file can be days old by the time it is read, and the kernel may have
// handed that number to something else entirely.
func isCollectorProcess(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "otelcol")
}

// awaitPortFree waits for the stopping collector to release its OTLP port, since
// the replacement fails to bind if it starts too soon.
func awaitPortFree(port int) error {
	for i := 0; i < 50; i++ {
		if !portListening(port) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the collector still holds port %d after 5s", port)
}

func portListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// StopCollector reads the PID file and sends SIGTERM to the collector process.
func StopCollector() error {
	pidPath := collectorPIDFile()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // already stopped
		}
		return fmt.Errorf("can not read the collector PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("the collector PID file holds a PID that is not valid: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		// Process not found — clean up PID file
		os.Remove(pidPath)
		return nil
	}

	if err := proc.Signal(os.Interrupt); err != nil {
		// Process may have already exited
		os.Remove(pidPath)
		return nil
	}

	os.Remove(pidPath)
	return nil
}

// CollectorRunning returns true if the host collector process is alive.
func CollectorRunning() bool {
	data, err := os.ReadFile(collectorPIDFile())
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}

	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	_, err = os.Stat(statusPath)
	return err == nil
}
