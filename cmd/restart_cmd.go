package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tilt"
)

var restartCmd = &cobra.Command{
	Use:   "restart [service|group]",
	Short: "Restart a running service or group",
	Long: `Restart a named service or group by re-triggering it (a disabled service is
re-enabled first). Unlike 'start', restart acts only on the target itself — it
does not re-trigger the target's dependencies.

If no name is given, devstack auto-detects the service from the current directory.
Accepts a service name or group name. Run 'devstack groups' to see available groups.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
	restartCmd.Flags().String("stack", "", "Target a feature stack's daemon instead of the base workspace")
}

func runRestart(cmd *cobra.Command, args []string) error {
	ws, env, envName, err := resolveWorkspaceAndEnv()
	if err != nil {
		return err
	}
	if err := requireLocalEnv(envName, env); err != nil {
		return err
	}

	stackName, _ := cmd.Flags().GetString("stack")
	wsPath, tiltPort, label, err := resolveStackTarget(ws, stackName)
	if err != nil {
		return err
	}

	cfg, err := config.Load(wsPath)
	if err != nil {
		return err
	}

	targetName := ""
	if len(args) > 0 {
		targetName = args[0]
	}

	services, err := resolveTarget(wsPath, targetName, cfg)
	if err != nil {
		return err
	}
	if targetName == "" {
		fmt.Printf("Auto-detected: %s\n", strings.Join(services, ", "))
	}
	if label != "" {
		fmt.Printf("Target: %s (:%d)\n", label, tiltPort)
	}

	tiltClient := tilt.NewClient("localhost", tiltPort)
	view, err := tiltClient.GetView()
	if err != nil {
		return fmt.Errorf("dev daemon is not running — start it first with: devstack up\n(%w)", err)
	}

	disabled := map[string]bool{}
	for _, r := range view.UiResources {
		if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
			disabled[r.Metadata.Name] = true
		}
	}

	var restarted []string
	for _, svc := range services {
		resolved, err := tilt.ResolveService(svc, view)
		if err != nil {
			return fmt.Errorf("could not resolve service %q: %w", svc, err)
		}
		// Re-enable a disabled service before triggering it.
		if disabled[resolved] {
			if out, err := tiltClient.RunCLI("enable", resolved); err != nil {
				return fmt.Errorf("enable %s failed: %v\n%s", resolved, err, out)
			}
		}
		if out, err := tiltClient.RunCLI("trigger", resolved); err != nil {
			return fmt.Errorf("failed to restart %q: %v\n%s", resolved, err, out)
		}
		restarted = append(restarted, resolved)
	}

	fmt.Printf("✓ Restarted: %s\n", strings.Join(restarted, ", "))
	return nil
}
