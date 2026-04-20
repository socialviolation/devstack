package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/tilt"
)

var stopCmd = &cobra.Command{
	Use:   "stop [service|group]",
	Short: "Stop a running service or group",
	Long: `Disable and stop a named service or group. Stopped services will not restart until
explicitly started again with 'devstack start'.

If no service name is given, devstack auto-detects the service from the current
directory by matching against registered service paths in .devstack.json.

Accepts a service name or group name. Run 'devstack groups' to see available groups.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStop,
}

func init() {
	rootCmd.AddCommand(stopCmd)
}

func runStop(cmd *cobra.Command, args []string) error {
	ws, env, envName, err := resolveWorkspaceAndEnv()
	if err != nil {
		return err
	}
	if err := requireLocalEnv(envName, env); err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	// Resolve target name
	targetName := ""
	if len(args) > 0 {
		targetName = args[0]
	}

	services, err := resolveTarget(targetName, cfg)
	if err != nil {
		return err
	}

	if targetName == "" {
		fmt.Printf("Auto-detected service: %s\n", strings.Join(services, ", "))
	}

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	view, err := tiltClient.GetView()
	if err != nil {
		return fmt.Errorf("dev daemon is not running — start it first with: devstack workspace up\n(%w)", err)
	}

	var stopped []string
	for _, svc := range services {
		resolved, err := tilt.ResolveService(svc, view)
		if err != nil {
			return fmt.Errorf("could not resolve service %q: %w", svc, err)
		}

		out, err := tiltClient.RunCLI("disable", resolved)
		if err != nil {
			return fmt.Errorf("failed to stop %q: %v\n%s", resolved, err, out)
		}
		if out != "" {
			fmt.Print(out)
		}
		stopped = append(stopped, resolved)
	}

	fmt.Printf("✓ Stopped: %s\n", strings.Join(stopped, ", "))
	return nil
}
