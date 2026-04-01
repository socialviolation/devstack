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
	Use:   "stop [service]",
	Short: "Stop a service in the workspace",
	Long: `Stop a service managed by Tilt. If no service name is given, uses the default service
(DEVSTACK_DEFAULT_SERVICE or --default-service flag).

Use --if-last-session to skip stopping when other Claude sessions are still active
in the same workspace (used by the Claude Stop hook).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStop,
}

func init() {
	tiltCmd.AddCommand(stopCmd)
	stopCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	stopCmd.Flags().Bool("if-last-session", false, "Only stop if this is the last active Claude session in the workspace")
}

func runStop(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	ifLastSession, _ := cmd.Flags().GetBool("if-last-session")

	// Resolve service name
	service := ""
	if len(args) > 0 {
		service = args[0]
	} else {
		service = viper.GetString("default_service")
		if service == "" {
			return fmt.Errorf("no service specified: pass a service name or set DEVSTACK_DEFAULT_SERVICE")
		}
	}

	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	// --if-last-session check
	if ifLastSession {
		count, err := countClaudeSessionsInWorkspace(ws.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not check active Claude sessions: %v — proceeding with stop\n", err)
		} else if count > 1 {
			fmt.Fprintf(os.Stderr, "Other Claude sessions active — skipping service stop.\n")
			return nil
		}
	}

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	view, err := tiltClient.GetView()
	if err != nil {
		return fmt.Errorf("Tilt is not running: %w", err)
	}

	resolved, err := tilt.ResolveService(service, view)
	if err != nil {
		return fmt.Errorf("could not resolve service %q: %w", service, err)
	}

	out, err := tiltClient.RunCLI("disable", resolved)
	if err != nil {
		return fmt.Errorf("failed to stop %q: %v\n%s", resolved, err, out)
	}

	if out != "" {
		fmt.Print(out)
	}
	fmt.Printf("✓ Stopped: %s\n", resolved)
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
		name := entry.Name()
		if !isNumeric(name) {
			continue
		}

		cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%s/cmdline", name))
		if err != nil {
			continue
		}
		cmdline := strings.ReplaceAll(string(cmdlineBytes), "\x00", " ")
		if !strings.Contains(cmdline, "claude") {
			continue
		}

		cwd, err := os.Readlink(fmt.Sprintf("/proc/%s/cwd", name))
		if err != nil {
			continue
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
