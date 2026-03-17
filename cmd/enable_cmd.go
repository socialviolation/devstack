package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/tilt"
)

var svcStartCmd = &cobra.Command{
	Use:   "start [service]",
	Short: "Start a service (and its dependencies) in the workspace",
	Long: `Start a service by resolving its dependencies and triggering them all via Tilt.

Deps are started first (in dependency order), then the requested service.
Use --group to start all services in a named group.`,
	RunE: runEnable,
}

func init() {
	rootCmd.AddCommand(svcStartCmd)
	svcStartCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	svcStartCmd.Flags().String("group", "", "Start a named group of services instead of a single service")
}

func runEnable(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	groupFlag, _ := cmd.Flags().GetString("group")

	if groupFlag == "" && len(args) == 0 {
		return fmt.Errorf("must specify a service name or --group=<name>\nUsage: devstack start <service>\n       devstack start --group=<name>")
	}

	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	var toTrigger []string

	if groupFlag != "" {
		// Expand group to services, resolve deps for each, union the sets
		services, ok := cfg.Groups[groupFlag]
		if !ok {
			return fmt.Errorf("group %q not found in .devstack.json", groupFlag)
		}

		seen := map[string]bool{}
		for _, svc := range services {
			resolved, err := config.ResolveDeps(cfg, svc)
			if err != nil {
				return err
			}
			for _, r := range resolved {
				if !seen[r] {
					seen[r] = true
					toTrigger = append(toTrigger, r)
				}
			}
		}
	} else {
		service := args[0]
		resolved, err := config.ResolveDeps(cfg, service)
		if err != nil {
			return err
		}
		toTrigger = resolved
	}

	fmt.Printf("Starting: %s\n", strings.Join(toTrigger, ", "))

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	for _, svc := range toTrigger {
		out, err := tiltClient.RunCLI("trigger", svc)
		if err != nil {
			if out != "" {
				fmt.Print(out)
			}
			return fmt.Errorf("tilt trigger %s failed: %w", svc, err)
		}
		if out != "" {
			fmt.Print(out)
		}
	}
	fmt.Printf("✓ Started: %s\n", strings.Join(toTrigger, ", "))

	return nil
}
