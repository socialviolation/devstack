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
	Short: "Start a service and all its dependencies",
	Long: `Start a service by name, automatically resolving and starting its dependencies first.

devstack reads the dependency graph from .devstack.json and computes the correct
startup order. Dependencies are enabled and triggered before the requested service,
so you never have to think about ordering.

If no service name is given, devstack will auto-detect it from the current directory
by matching against registered service paths in .devstack.json.

Use --group to start every service in a named group at once (also resolves deps).

Requires the dev daemon to be running first:
  devstack workspace up`,
	RunE: runEnable,
}

func init() {
	rootCmd.AddCommand(svcStartCmd)
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
	wsFlag, _ := cmd.Flags().GetString("workspace") // inherited persistent flag
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
		return fmt.Errorf("dev daemon is not running — start it first with: devstack workspace up\n(%w)", err)
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
				return fmt.Errorf("enable %s failed: %w", svc, err)
			}
		}
		out, err := tiltClient.RunCLI("trigger", svc)
		if err != nil {
			if out != "" {
				fmt.Print(out)
			}
			return fmt.Errorf("trigger %s failed: %w", svc, err)
		}
		if out != "" {
			fmt.Print(out)
		}
	}
	fmt.Printf("✓ Started: %s\n", strings.Join(toTrigger, ", "))

	return nil
}
