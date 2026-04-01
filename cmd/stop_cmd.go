package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/tilt"
)

var stopCmd = &cobra.Command{
	Use:   "stop [service]",
	Short: "Stop a running service",
	Long: `Disable and stop a named service. The service will not restart until explicitly
started again with 'devstack start'.

If no service name is given, devstack auto-detects the service from the current
directory by matching against registered service paths in .devstack.json.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runStop,
}

func init() {
	rootCmd.AddCommand(stopCmd)
}

func runStop(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace") // inherited persistent flag

	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	// Resolve service name
	service := ""
	if len(args) > 0 {
		service = args[0]
	} else {
		cfg, err := config.Load(ws.Path)
		if err != nil {
			return err
		}
		service, err = detectServiceFromCwd(cfg)
		if err != nil {
			return err
		}
		fmt.Printf("Auto-detected service: %s\n", service)
	}

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	view, err := tiltClient.GetView()
	if err != nil {
		return fmt.Errorf("dev daemon is not running — start it first with: devstack workspace up\n(%w)", err)
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
