package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop the dev daemon for the current workspace",
	Long: `Stop the dev daemon and all locally running services for the current workspace.

Also stops the managed SigNoz observability stack if it is running.
The PID file is removed. Run 'devstack workspace up' to start again.`,
	RunE: runDown,
}

var downAliasCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop the dev daemon for the current workspace (alias for: devstack workspace down)",
	Long: `Stop the dev daemon and all locally running services for the current workspace.

Also stops the managed SigNoz observability stack if it is running.
The PID file is removed. Run 'devstack up' to start again.

Use --all to stop every running workspace at once.`,
	RunE: runDown,
}

func init() {
	workspaceCmd.AddCommand(downCmd)
	rootCmd.AddCommand(downAliasCmd)

	downCmd.Flags().Bool("all", false, "Stop all running workspaces")
	downAliasCmd.Flags().Bool("all", false, "Stop all running workspaces")
}

func runDown(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	if all {
		return runDownAll()
	}

	ws, env, envName, err := resolveWorkspaceAndEnv()
	if err != nil {
		return err
	}
	if err := requireLocalEnv(envName, env); err != nil {
		return err
	}

	pidFile := workspace.PIDFile(ws.Name)

	// 2. Read PID from PID file
	pidData, pidErr := os.ReadFile(pidFile)
	if pidErr != nil {
		// No PID file — check if daemon is reachable anyway
		apiURL := fmt.Sprintf("http://localhost:%d/api/view", ws.TiltPort)
		if !isTiltReachable(apiURL) {
			fmt.Printf("Dev daemon is not running for '%s'\n", ws.Name)
			return nil
		}
		fmt.Fprintf(os.Stderr, "Warning: no PID file found but daemon is reachable — it may have been started outside devstack\n")
		fmt.Printf("  ✓ Tilt stopped\n")
		return nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return fmt.Errorf("invalid PID in file %s: %w", pidFile, err)
	}

	fmt.Printf("Stopping %s (pid %d)...\n", ws.Name, pid)

	// 3. Kill the process
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not find process %d: %v\n", pid, err)
	} else {
		if killErr := proc.Kill(); killErr != nil {
			if isProcessAlive(pid) {
				fmt.Fprintf(os.Stderr, "Warning: failed to kill process %d: %v\n", pid, killErr)
			}
			// Process may have already exited — not fatal
		}
	}

	// 4. Remove PID file
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: failed to remove PID file: %v\n", err)
	}

	fmt.Printf("  ✓ Tilt stopped\n")

	// 5. Stop observability stack
	if isOtelRunning(ws) {
		localEnv, _ := ws.ResolveEnvironment("local")
		plugin := activePlugin(ws, localEnv)
		if err := stopOtelStack(ws, plugin); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: OTEL stop failed: %v\n", err)
		} else {
			fmt.Printf("  ✓ OTEL stopped\n")
		}
	} else {
		fmt.Printf("  OTEL not running\n")
	}

	return nil
}

func runDownAll() error {
	workspaces, err := workspace.All()
	if err != nil {
		return err
	}

	anyRunning := false
	for _, ws := range workspaces {
		pidFile := workspace.PIDFile(ws.Name)
		pidData, err := os.ReadFile(pidFile)
		if err != nil {
			continue // no pid file = not managed by devstack
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err != nil || !isProcessAlive(pid) {
			continue
		}

		anyRunning = true
		fmt.Printf("Stopping %s (pid %d)...\n", ws.Name, pid)

		proc, err := os.FindProcess(pid)
		if err == nil {
			proc.Kill()
		}
		os.Remove(pidFile)
		fmt.Printf("  ✓ Tilt stopped\n")

		if isOtelRunning(&ws) {
			localEnv, _ := ws.ResolveEnvironment("local")
			plugin := activePlugin(&ws, localEnv)
			if err := stopOtelStack(&ws, plugin); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: OTEL stop failed: %v\n", err)
			} else {
				fmt.Printf("  ✓ OTEL stopped\n")
			}
		}
	}

	if !anyRunning {
		fmt.Println("No workspaces running.")
	}
	return nil
}
