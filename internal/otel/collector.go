package otel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"devstack/internal/workspace"
)

// collectorPIDFile returns the path to the collector PID file for a workspace.
func collectorPIDFile(ws *workspace.Workspace) string {
	return filepath.Join(workspace.DataDir(ws.Name), "collector.pid")
}

// collectorConfigPath returns the path where the collector config is written.
func collectorConfigPath(ws *workspace.Workspace) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "devstack", "collector", ws.Name, "config.yaml"), nil
}

// otelcolBin returns the path to the otelcol-contrib binary.
// Checks OTELCOL_BIN env var first, then PATH.
func otelcolBin() (string, error) {
	if bin := os.Getenv("OTELCOL_BIN"); bin != "" {
		return bin, nil
	}
	path, err := exec.LookPath("otelcol-contrib")
	if err != nil {
		return "", fmt.Errorf(`otelcol-contrib not found. Install it:
  macOS:  brew install opentelemetry-collector-contrib
  Linux:  https://github.com/open-telemetry/opentelemetry-collector-releases/releases
Or set OTELCOL_BIN=/path/to/binary`)
	}
	return path, nil
}

// receiverBlock returns the OTLP receiver YAML block for the given workspace ports.
func receiverBlock(ws *workspace.Workspace) string {
	return fmt.Sprintf(`receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:%d
      http:
        endpoint: 0.0.0.0:%d
`, ws.GRPCPort(), ws.HTTPPort())
}

// StartCollector generates the full collector config and spawns otelcol-contrib.
func StartCollector(ws *workspace.Workspace, plugin Plugin) error {
	bin, err := otelcolBin()
	if err != nil {
		return err
	}

	pluginConfig, err := plugin.CollectorConfig(ws)
	if err != nil {
		return fmt.Errorf("plugin %q config error: %w", plugin.Name(), err)
	}

	// Merge receiver block + plugin config
	fullConfig := receiverBlock(ws) + "\n" + string(pluginConfig)

	// Write config to disk
	cfgPath, err := collectorConfigPath(ws)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return fmt.Errorf("failed to create collector config dir: %w", err)
	}
	if err := os.WriteFile(cfgPath, []byte(fullConfig), 0644); err != nil {
		return fmt.Errorf("failed to write collector config: %w", err)
	}

	// Ensure data dir exists for PID file
	pidPath := collectorPIDFile(ws)
	if err := os.MkdirAll(filepath.Dir(pidPath), 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}

	// Spawn otelcol-contrib as background process
	cmd := exec.Command(bin, "--config="+cfgPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start otelcol-contrib: %w", err)
	}

	// Write PID file
	pid := cmd.Process.Pid
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		// Don't fail — process is running, just can't track it
		fmt.Fprintf(os.Stderr, "warning: failed to write collector PID file: %v\n", err)
	}

	return nil
}

// StopCollector reads the PID file and sends SIGTERM to the collector process.
func StopCollector(ws *workspace.Workspace) error {
	pidPath := collectorPIDFile(ws)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // already stopped
		}
		return fmt.Errorf("failed to read collector PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("invalid PID in collector PID file: %w", err)
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

// CollectorRunning returns true if the collector process is alive.
func CollectorRunning(ws *workspace.Workspace) bool {
	pidPath := collectorPIDFile(ws)
	data, err := os.ReadFile(pidPath)
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
