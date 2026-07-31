package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tilt"
)

func runEnable(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace") // inherited persistent flag

	ws, err := resolveWorkspace(wsFlag)
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

	targetName := ""
	if len(args) > 0 {
		targetName = args[0]
	}

	services, err := resolveTargetKind(wsPath, targetName, cfg, targetKindOf(cmd))
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
	if label != "" {
		fmt.Printf("Target: %s (:%d)\n", label, tiltPort)
	}

	tiltClient := tilt.NewClient("localhost", tiltPort)
	syncHostTiltfile(tiltClient)

	view, err := tiltClient.GetView()
	if err != nil {
		if stackName != "" {
			return fmt.Errorf("dev daemon not reachable on :%d — start the stack first with: devstack stack up %s\n(%w)", tiltPort, stackName, err)
		}
		// Daemon not running — bring it up automatically, then retry. runStart is
		// idempotent and self-resolves the workspace, so this is a no-op if it's
		// already up by the time we get here.
		fmt.Println("Dev daemon not running — starting it...")
		if startErr := runStart(cmd, args); startErr != nil {
			return fmt.Errorf("failed to auto-start dev daemon: %w", startErr)
		}
		view, err = tiltClient.GetView()
		if err != nil {
			return fmt.Errorf("dev daemon started but not reachable yet — retry: devstack service start %s\n(%w)", targetName, err)
		}
	}

	// Build sets of disabled and present resources for quick lookup
	disabled := map[string]bool{}
	present := map[string]bool{}
	for _, r := range view.UiResources {
		present[r.Metadata.Name] = true
		if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
			disabled[r.Metadata.Name] = true
		}
	}

	for _, svc := range toTrigger {
		rn := resourceName(ws.Name, svc, namespace)
		if stackName != "" && !present[rn] {
			fmt.Printf("  (dep %s runs in base — not started here)\n", svc)
			continue
		}
		if disabled[rn] {
			if out, err := tiltClient.RunCLI("enable", rn); err != nil {
				if out != "" {
					fmt.Print(out)
				}
				return fmt.Errorf("enable %s failed: %w", rn, err)
			}
		}
		out, err := tiltClient.RunCLI("trigger", rn)
		if err != nil {
			if out != "" {
				fmt.Print(out)
			}
			return fmt.Errorf("trigger %s failed: %w", rn, err)
		}
		if out != "" {
			fmt.Print(out)
		}
	}
	fmt.Printf("✓ Started: %s\n", strings.Join(toTrigger, ", "))

	return fireHooks(ws, stackName, config.EventServiceStart, toTrigger)
}
