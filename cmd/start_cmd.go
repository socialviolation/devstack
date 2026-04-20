package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
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

var upAliasCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the dev daemon for the current workspace (alias for: devstack workspace up)",
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
	rootCmd.AddCommand(upAliasCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	ws, env, envName, err := resolveWorkspaceAndEnv()
	if err != nil {
		return err
	}
	if err := requireLocalEnv(envName, env); err != nil {
		return err
	}

	pidFile := workspace.PIDFile(ws.Name)
	logFile := workspace.LogFile(ws.Name)
	dataDir := workspace.DataDir(ws.Name)

	// 2. Check if daemon already running via API
	apiURL := fmt.Sprintf("http://localhost:%d/api/view", ws.TiltPort)
	if isTiltReachable(apiURL) {
		fmt.Printf("Dev daemon already running for '%s'\n", ws.Name)
		return nil
	}

	// 3. Check if PID file exists and process is alive
	if pidData, err := os.ReadFile(pidFile); err == nil {
		pid, parseErr := strconv.Atoi(string(pidData))
		if parseErr == nil {
			if isProcessAlive(pid) {
				// Sync registry port with the actual running port
				if actual := workspace.ResolvePort(ws.Name); actual != 0 && actual != ws.TiltPort {
					ws.TiltPort = actual
					fmt.Printf("Updated workspace port to %d (was stale)\n", actual)
				}
				fmt.Printf("Dev daemon already running (pid %d, port %d)\n", pid, ws.TiltPort)
				return nil
			}
			// Stale PID file — remove it
			os.Remove(pidFile)
		}
	}

	// 4. Create data dir
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory %s: %w", dataDir, err)
	}

	// 5. Open log file
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", logFile, err)
	}
	defer lf.Close()

	// 6. Start daemon
	tiltCmd := exec.Command("tilt", "up", "--host", "0.0.0.0", "--port", strconv.Itoa(ws.TiltPort))
	tiltCmd.Dir = ws.Path
	tiltCmd.Stdout = lf
	tiltCmd.Stderr = lf
	tiltCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := tiltCmd.Start(); err != nil {
		return fmt.Errorf("failed to start dev daemon: %w", err)
	}

	pid := tiltCmd.Process.Pid

	// 7. Write PID file
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		// Kill the process we just started if we can't track it
		tiltCmd.Process.Kill()
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	// 8. Poll until daemon is reachable (up to 45s, every 2s)
	fmt.Printf("Starting dev daemon for '%s'", ws.Name)
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

	// 9. Print result
	if reached {
		fmt.Printf("✓ Started (pid %d, port %d, logs: %s)\n", pid, ws.TiltPort, logFile)
	} else {
		fmt.Printf("Started but not yet reachable — logs: %s\n", logFile)
	}

	// 10. Start observability backend
	if isOtelRunning(ws) {
		fmt.Printf("OTEL stack already running\n")
	} else {
		plugin := activePlugin(ws)
		if plugin == nil {
			fmt.Fprintf(os.Stderr, "No OTEL plugin configured\n")
		} else {
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

