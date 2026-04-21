package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/tilt"
)

var svcStartCmd = &cobra.Command{
	Use:   "start [service|group]",
	Short: "Start a service or group and all its dependencies",
	Long: `Start a service or group by name, automatically resolving and starting its dependencies first.

devstack reads the dependency graph from .devstack.json and computes the correct
startup order. Dependencies are enabled and triggered before the requested service,
so you never have to think about ordering.

If no service name is given, devstack will auto-detect it from the current directory
by matching against registered service paths in .devstack.json.

Accepts a service name or group name. Run 'devstack groups' to see available groups.

Requires the dev daemon to be running first:
  devstack workspace up`,
	RunE: runEnable,
}

func init() {
	rootCmd.AddCommand(svcStartCmd)
	f := svcStartCmd.Flags().Lookup("group")
	if f == nil {
		svcStartCmd.Flags().String("group", "", "Start a named group of services (hidden alias: pass group name as positional arg instead)")
	}
	svcStartCmd.Flags().MarkHidden("group")
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

	// Determine the target name: --group flag is a hidden alias for the positional arg
	targetName := ""
	if len(args) > 0 {
		targetName = args[0]
	} else if groupFlag != "" {
		targetName = groupFlag
	}

	// Resolve target to a list of services (service or group)
	services, err := resolveTarget(ws.Path, targetName, cfg)
	if err != nil {
		return err
	}

	if targetName == "" {
		fmt.Printf("Auto-detected: %s\n", strings.Join(services, ", "))
	}

	// Expand deps for each resolved service, union the sets
	var toTrigger []string
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
