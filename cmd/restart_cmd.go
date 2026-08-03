package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
)

func runRestart(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}

	flagStack, _ := cmd.Flags().GetString("stack")
	stackName, err := stack.ResolveTarget(ws, flagStack)
	if err != nil {
		return err
	}
	tiltPort, namespace, wsPath, label, err := resolveStackTarget(ws, stackName)
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

	services, err := resolveInstanceTarget(cmd, ws, wsPath, targetName, cfg, stackName)
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
	syncHostTiltfile(tiltClient)

	view, err := tiltClient.GetView()
	if err != nil {
		return fmt.Errorf("dev daemon is not running — start it first with: devstack workspace up\n(%w)", err)
	}

	disabled := map[string]bool{}
	for _, r := range view.UiResources {
		if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
			disabled[r.Metadata.Name] = true
		}
	}

	var restarted []string
	for _, svc := range services {
		resolved, err := tilt.ResolveService(resourceName(ws.Name, svc, namespace), view)
		if err != nil {
			return fmt.Errorf("can not resolve service %q: %w", svc, err)
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
