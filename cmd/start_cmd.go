package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/workspace"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the dev daemon for the current workspace",
	Long: `Start the dev daemon as a detached background process for the current workspace.

The dev daemon is responsible for running all local services, watching source
files for changes, and hot-reloading services when code is modified. It must
be running before you can start, stop, or restart individual services.

The SigNoz observability stack is also started automatically so services can
begin shipping traces and logs immediately.

Logs are written to ~/.local/share/devstack/<workspace-name>/tilt.log.`,
	RunE: runStart,
}

func init() {
	workspaceCmd.AddCommand(upCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	ws, env, envName, err := resolveWorkspaceAndEnv()
	if err != nil {
		return err
	}
	if err := requireLocalEnv(envName, env); err != nil {
		return err
	}
	if !config.HasWorkspaceManifest(ws.Path) {
		return fmt.Errorf("no %s in %s — this workspace isn't manifest-based yet", config.WorkspaceManifestFileName, ws.Path)
	}

	if err := workspace.SetWorkspaceActive(ws.Name, true); err != nil {
		return fmt.Errorf("failed to mark workspace active: %w", err)
	}
	if _, err := regenerateHostTiltfile(); err != nil {
		return fmt.Errorf("failed to generate host Tiltfile: %w", err)
	}
	if err := ensureHostDaemon(); err != nil {
		return err
	}
	fmt.Printf("Service(s) for '%s' run in the host daemon on :%d as %s:<svc>.\n", ws.Name, workspace.HostTiltPort, ws.Name)

	if composeSpec, err := infra.ResolveComposeSpec(ws.Path); err != nil {
		fmt.Fprintf(os.Stderr, "compose infra config error: %v\n", err)
	} else if composeSpec != nil {
		fmt.Printf("Starting compose infra...\n")
		if err := infra.Up(composeSpec); err != nil {
			fmt.Fprintf(os.Stderr, "compose infra failed: %v\n", err)
		} else {
			fmt.Printf("✓ Compose infra started\n")
		}
	}

	// 10. Start observability backend — only when the workspace opts in.
	// We don't assume services are OTEL-instrumented; enable it in the workspace
	// manifest (observability.enabled) to run a collector and ship telemetry.
	// A feature stack never runs its own collector — it attaches to the base's
	// (generation points its OTEL endpoint there), and two collectors cannot bind
	// the same host ports anyway.
	if !config.ObservabilityEnabled(ws.Path) {
		fmt.Printf("Observability disabled for this workspace — skipping collector.\n")
		fmt.Printf("  Enable it: set observability.enabled: true in %s, then: devstack otel start\n", config.WorkspaceManifestFileName)
	} else if isOtelRunning(ws) {
		fmt.Printf("OTEL stack already running\n")
	} else {
		plugin := activePlugin(ws, env)
		if plugin == nil {
			fmt.Fprintf(os.Stderr, "No OTEL plugin configured\n")
		} else {
			// If the environment drives forwarding mode, populate plugin config from env
			// into a local (in-memory only) copy of the workspace. Never saved to disk.
			if env.Observability.OTLPEndpoint != "" && ws.OtelPlugin == "" {
				wsCopy := *ws
				if wsCopy.OtelPluginConfig == nil {
					wsCopy.OtelPluginConfig = map[string]string{}
				} else {
					copied := make(map[string]string, len(wsCopy.OtelPluginConfig))
					for k, v := range wsCopy.OtelPluginConfig {
						copied[k] = v
					}
					wsCopy.OtelPluginConfig = copied
				}
				wsCopy.OtelPluginConfig["upstream"] = env.Observability.OTLPEndpoint
				if env.Observability.APIKey != "" {
					wsCopy.OtelPluginConfig["api_key"] = env.Observability.APIKey
				}
				wsCopy.OtelPluginConfig["deployment_env"] = envName
				ws = &wsCopy
			}
			fmt.Printf("Starting OTEL stack (plugin: %s)...\n", plugin.Name())
			if err := startOtelStack(ws, plugin); err != nil {
				fmt.Fprintf(os.Stderr, "OTEL stack failed: %v\n", err)
			} else {
				queryEndpoint := plugin.QueryEndpoint(ws)
				if queryEndpoint != "" {
					fmt.Printf("✓ OTEL %s\n", queryEndpoint)
				} else {
					fmt.Printf("✓ OTEL collector running (plugin: %s)\n", plugin.Name())
				}
			}
		}
	}

	return nil
}

// ensureHostDaemon starts the one host Tilt daemon if it is not already running.
// If it is reachable or its PID is alive, it is left as-is (a running daemon
// hot-reloads the host Tiltfile the caller just regenerated). Otherwise it starts
// `tilt up` in the host Tilt dir on the fixed host port, tracks its PID and
// session, and polls until reachable.
func ensureHostDaemon() error {
	apiURL := fmt.Sprintf("http://localhost:%d/api/view", workspace.HostTiltPort)
	if isTiltReachable(apiURL) {
		fmt.Printf("Host daemon already running on :%d — it will hot-reload the updated Tiltfile.\n", workspace.HostTiltPort)
		return nil
	}

	pidFile := workspace.HostPIDFile()
	if pidData, err := os.ReadFile(pidFile); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(pidData))); perr == nil && isProcessAlive(pid) {
			fmt.Printf("Host daemon already running (pid %d, port %d).\n", pid, workspace.HostTiltPort)
			return nil
		}
		os.Remove(pidFile)
	}

	dir := workspace.HostTiltDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create host data directory %s: %w", dir, err)
	}

	logFile := workspace.HostLogFile()
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open host log file %s: %w", logFile, err)
	}
	defer lf.Close()

	tiltCmd := exec.Command("tilt", "up", "--host", "0.0.0.0", "--port", strconv.Itoa(workspace.HostTiltPort))
	tiltCmd.Dir = dir
	tiltCmd.Stdout = lf
	tiltCmd.Stderr = lf
	tiltCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := tiltCmd.Start(); err != nil {
		return fmt.Errorf("failed to start host daemon: %w", err)
	}

	pid := tiltCmd.Process.Pid
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		tiltCmd.Process.Kill()
		return fmt.Errorf("failed to write host PID file: %w", err)
	}
	_, _ = workspace.OpenSession(workspace.HostWorkspace(), pid, []int{workspace.HostTiltPort})

	fmt.Printf("Starting host daemon")
	deadline := time.Now().Add(45 * time.Second)
	reached := false
	for time.Now().Before(deadline) {
		if isTiltReachable(apiURL) {
			reached = true
			break
		}
		fmt.Print(".")
		time.Sleep(2 * time.Second)
	}
	fmt.Println()

	if reached {
		fmt.Printf("✓ Host daemon started (pid %d, port %d, logs: %s)\n", pid, workspace.HostTiltPort, logFile)
	} else {
		fmt.Printf("Host daemon started but not yet reachable — logs: %s\n", logFile)
	}
	return nil
}

// resolveWorkspace resolves a workspace by name/path flag or auto-detects from cwd.
func resolveWorkspace(flag string) (*workspace.Workspace, error) {
	if flag == "" {
		return workspace.DetectFromCwd()
	}

	// Try by name first, then by path
	ws, err := workspace.FindByName(flag)
	if err == nil {
		return ws, nil
	}

	ws, err = workspace.FindByPath(flag)
	if err == nil {
		return ws, nil
	}

	return nil, fmt.Errorf("workspace %q not found by name or path", flag)
}

// isTiltReachable returns true if the Tilt API is responding at the given URL.
func isTiltReachable(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// isProcessAlive returns true if a process with the given PID exists and is running.
func isProcessAlive(pid int) bool {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	_, err := os.Stat(statusPath)
	return err == nil
}
