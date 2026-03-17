package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"devstack/internal/tilt"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop a service in the dev stack",
	Long:  `Stop (disable) a service managed by Tilt. Use --if-last-session to skip stopping when other Claude sessions are active.`,
	RunE:  runStop,
}

func init() {
	rootCmd.AddCommand(stopCmd)
	stopCmd.Flags().Bool("if-last-session", false, "Only stop if this is the last active Claude session in the workspace")
}

func runStop(cmd *cobra.Command, args []string) error {
	defaultService := viper.GetString("default_service")
	workspace := viper.GetString("workspace")
	ifLastSession, _ := cmd.Flags().GetBool("if-last-session")

	if defaultService == "" {
		return fmt.Errorf("no service specified: use --default-service or DEVSTACK_DEFAULT_SERVICE")
	}

	if ifLastSession && workspace != "" {
		count, err := countClaudeSessionsInWorkspace(workspace)
		if err != nil {
			// Non-fatal: if we can't check, proceed with stop
			fmt.Fprintf(os.Stderr, "Warning: could not check active Claude sessions: %v — proceeding with stop\n", err)
		} else if count > 1 {
			fmt.Fprintf(os.Stderr, "Other Claude sessions active — skipping service stop.\n")
			return nil
		}
	}

	tiltClient := tilt.NewClient(
		viper.GetString("tilt.host"),
		viper.GetInt("tilt.port"),
	)

	view, err := tiltClient.GetView()
	if err != nil {
		return fmt.Errorf("Tilt is not running: %w", err)
	}

	resolved, err := tilt.ResolveService(defaultService, view)
	if err != nil {
		return fmt.Errorf("could not resolve service %q: %w", defaultService, err)
	}

	out, err := tiltClient.RunCLI("disable", resolved)
	if err != nil {
		return fmt.Errorf("failed to stop %q: %v\n%s", resolved, err, out)
	}

	fmt.Fprintf(os.Stderr, "Stopped service %q.\n%s", resolved, out)
	return nil
}

// countClaudeSessionsInWorkspace counts how many "claude" processes have their
// cwd under the given workspace directory by reading /proc on Linux.
func countClaudeSessionsInWorkspace(workspace string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("could not read /proc: %w", err)
	}

	count := 0
	for _, entry := range entries {
		// Only numeric directories are PIDs
		name := entry.Name()
		if !isNumeric(name) {
			continue
		}

		// Check if the process cmdline contains "claude"
		cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%s/cmdline", name))
		if err != nil {
			continue // process may have exited or we lack permission
		}
		// cmdline uses null bytes as separators
		cmdline := strings.ReplaceAll(string(cmdlineBytes), "\x00", " ")
		if !strings.Contains(cmdline, "claude") {
			continue
		}

		// Check if the process cwd is under the workspace
		cwd, err := os.Readlink(fmt.Sprintf("/proc/%s/cwd", name))
		if err != nil {
			continue // skip if we can't read cwd (permission error etc.)
		}

		if strings.HasPrefix(cwd, workspace) {
			count++
		}
	}

	return count, nil
}

// isNumeric returns true if s consists entirely of ASCII digits.
func isNumeric(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
