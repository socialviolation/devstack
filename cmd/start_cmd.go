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

	"devstack/internal/workspace"
)

var upCmd = &cobra.Command{
	Use:     "up",
	Aliases: []string{"start"},
	Short:   "Start Tilt for a workspace as a background daemon",
	Long:    `Start Tilt (tilt up) as a detached background daemon for the given workspace. Logs are written to ~/.local/share/devstack/<name>/tilt.log and the PID is tracked in tilt.pid.`,
	RunE:    runStart,
}

func init() {
	rootCmd.AddCommand(upCmd)
	upCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
}

func runStart(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")

	// 1. Resolve workspace
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	pidFile := workspace.PIDFile(ws.Name)
	logFile := workspace.LogFile(ws.Name)
	dataDir := workspace.DataDir(ws.Name)

	// 2. Check if Tilt already running via API
	apiURL := fmt.Sprintf("http://localhost:%d/api/view", ws.TiltPort)
	if isTiltReachable(apiURL) {
		fmt.Printf("Tilt already running for '%s'\n", ws.Name)
		return nil
	}

	// 3. Check if PID file exists and process is alive
	if pidData, err := os.ReadFile(pidFile); err == nil {
		pid, parseErr := strconv.Atoi(string(pidData))
		if parseErr == nil {
			if isProcessAlive(pid) {
				// Sync registry port with the actual port Tilt is running on
				if actual := tiltPortFromPID(pid); actual != 0 && actual != ws.TiltPort {
					ws.TiltPort = actual
					if err := workspace.UpdatePort(ws.Name, actual); err == nil {
						fmt.Printf("Updated workspace port to %d (was stale)\n", actual)
					}
				}
				fmt.Printf("Tilt is already running (pid %d, port %d)\n", pid, ws.TiltPort)
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
		return fmt.Errorf("failed to start tilt: %w", err)
	}

	pid := tiltCmd.Process.Pid

	// 7. Write PID file
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		// Kill the process we just started if we can't track it
		tiltCmd.Process.Kill()
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	// 8. Poll until Tilt is reachable (up to 45s, every 2s)
	fmt.Printf("Starting Tilt for '%s'", ws.Name)
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
		fmt.Printf("✓ Tilt started (pid %d, port %d, logs: %s)\n", pid, ws.TiltPort, logFile)
	} else {
		fmt.Printf("Tilt started but not yet reachable — logs: %s\n", logFile)
	}

	// 10. Start Jaeger if not already running
	containerName := workspace.OtelContainerName(ws.Name)
	if isOtelRunning(containerName) {
		fmt.Printf("Jaeger already running\n")
	} else {
		fmt.Printf("Starting Jaeger...")
		if err := startOtel(containerName); err != nil {
			fmt.Fprintf(os.Stderr, " failed: %v\n", err)
		} else {
			fmt.Printf(" ✓ %s\n", otelUIURL)
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

// tiltPortFromPID reads /proc/<pid>/cmdline and extracts the --port value.
// Returns 0 if not found.
func tiltPortFromPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return 0
	}
	// cmdline is null-byte separated
	args := strings.Split(string(data), "\x00")
	for i, arg := range args {
		if arg == "--port" && i+1 < len(args) {
			if p, err := strconv.Atoi(args[i+1]); err == nil {
				return p
			}
		}
		if strings.HasPrefix(arg, "--port=") {
			if p, err := strconv.Atoi(strings.TrimPrefix(arg, "--port=")); err == nil {
				return p
			}
		}
	}
	return 0
}
