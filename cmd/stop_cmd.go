package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tilt"
)

func runStop(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}

	stackName, _ := cmd.Flags().GetString("stack")
	tiltPort, namespace, wsPath, label, err := resolveStackTarget(ws, stackName)
	if err != nil {
		return err
	}

	cfg, err := config.Load(wsPath)
	if err != nil {
		return err
	}

	// Resolve target name
	targetName := ""
	if len(args) > 0 {
		targetName = args[0]
	}

	services, err := resolveTargetKind(wsPath, targetName, cfg, targetKindOf(cmd))
	if err != nil {
		return err
	}

	if targetName == "" {
		fmt.Printf("Auto-detected service: %s\n", strings.Join(services, ", "))
	}
	if label != "" {
		fmt.Printf("Target: %s (:%d)\n", label, tiltPort)
	}

	tiltClient := tilt.NewClient("localhost", tiltPort)
	view, err := tiltClient.GetView()
	if err != nil {
		return fmt.Errorf("dev daemon is not running — start it first with: devstack workspace up\n(%w)", err)
	}

	var stopped []string
	for _, svc := range services {
		resolved, err := tilt.ResolveService(resourceName(ws.Name, svc, namespace), view)
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
	return fireHooks(ws, stackName, config.EventServiceStop, services)
}
