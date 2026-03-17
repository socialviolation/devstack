package cmd

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop Tilt for a workspace",
	Long:  `Kill the Tilt daemon for the given workspace and remove the PID file.`,
	RunE:  runDown,
}

func init() {
	rootCmd.AddCommand(downCmd)
	downCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
}

func runDown(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")

	// 1. Resolve workspace
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	pidFile := workspace.PIDFile(ws.Name)

	// 2. Read PID from PID file
	pidData, pidErr := os.ReadFile(pidFile)
	if pidErr != nil {
		// No PID file — check if Tilt is reachable anyway
		apiURL := fmt.Sprintf("http://localhost:%d/api/view", ws.TiltPort)
		if !isTiltReachable(apiURL) {
			fmt.Printf("Tilt is not running for '%s'\n", ws.Name)
			return nil
		}
		fmt.Fprintf(os.Stderr, "Warning: no PID file found but Tilt API is reachable — Tilt may have been started outside devstack\n")
		fmt.Printf("✓ Tilt stopped for '%s'\n", ws.Name)
		return nil
	}

	pid, err := strconv.Atoi(string(pidData))
	if err != nil {
		return fmt.Errorf("invalid PID in file %s: %w", pidFile, err)
	}

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

	fmt.Printf("✓ Tilt stopped for '%s'\n", ws.Name)
	return nil
}
