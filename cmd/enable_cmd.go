package cmd

import (
	"fmt"
	"os"
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
	tiltCmd.AddCommand(svcStartCmd)
	svcStartCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	svcStartCmd.Flags().String("group", "", "Start a named group of services instead of a single service")
}

func detectServiceFromCwd(cfg *config.WorkspaceConfig) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	var matches []string
	for name, path := range cfg.ServicePaths {
		if cwd == path || strings.HasPrefix(cwd, path+"/") {
			matches = append(matches, name)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("must specify a service name or --group=<name>\nUsage: devstack start <service>\n       devstack start --group=<name>")
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple services match the current directory (%s); please specify a service name explicitly", strings.Join(matches, ", "))
	}
}

func runEnable(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	groupFlag, _ := cmd.Flags().GetString("group")

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
		var service string
		if len(args) > 0 {
			service = args[0]
		} else {
			service, err = detectServiceFromCwd(cfg)
			if err != nil {
				return err
			}
			fmt.Printf("Auto-detected service: %s\n", service)
		}
		resolved, err := config.ResolveDeps(cfg, service)
		if err != nil {
			return err
		}
		toTrigger = resolved
	}

	fmt.Printf("Starting: %s\n", strings.Join(toTrigger, ", "))

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	view, err := tiltClient.GetView()
	if err != nil {
		return fmt.Errorf("failed to get Tilt view: %w", err)
	}

	// Build a set of disabled resources for quick lookup
	disabled := map[string]bool{}
	for _, r := range view.UiResources {
		if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
			disabled[r.Metadata.Name] = true
		}
	}

	for _, svc := range toTrigger {
		if disabled[svc] {
			if out, err := tiltClient.RunCLI("enable", svc); err != nil {
				if out != "" {
					fmt.Print(out)
				}
				return fmt.Errorf("tilt enable %s failed: %w", svc, err)
			}
		}
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
