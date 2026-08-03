package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
)

func runEnable(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace") // inherited persistent flag

	ws, err := resolveWorkspace(wsFlag)
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
		// Daemon not running — bring it up automatically, then retry.
		// bringWorkspaceUp is idempotent and self-resolves the workspace, so this
		// is a no-op if it is already up by the time we get here. Its hooks are
		// fired separately: a broken workspace.up hook is not a daemon failure,
		// and it must not abandon a service start whose daemon is up.
		fmt.Println("Dev daemon not running — starting it...")
		upWS, startErr := bringWorkspaceUp()
		var hookErr error
		if startErr == nil {
			hookErr = fireHooks(upWS, "", config.EventWorkspaceUp, nil)
		}
		fatal, warnings := autoStartOutcome(startErr, hookErr, services)
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		if fatal != nil {
			return fatal
		}
		view, err = tiltClient.GetView()
		if err != nil {
			return fmt.Errorf("dev daemon started but not reachable yet — retry: devstack service start %s --stack base\n(%w)", targetName, err)
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

	here, inBase := splitByPresence(ws.Name, namespace, stackName, toTrigger, present)
	for _, svc := range inBase {
		fmt.Printf("  (dep %s runs in base — not started here)\n", svc)
	}

	for _, svc := range here {
		rn := resourceName(ws.Name, svc, namespace)
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
	fmt.Printf("✓ Started: %s\n", strings.Join(here, ", "))

	// Only the services this command triggered get a service.start hook. A dep
	// that lives in base was deliberately left alone, and firing its hook here
	// both misreports what happened and fails the whole command when that hook
	// fails.
	return fireHooks(ws, stackName, config.EventServiceStart, here)
}

// splitByPresence divides the resolved service set into the ones this daemon
// target runs and the deps that live in the base workspace. A stack
// only holds the services it overlays, so its other deps are already running as
// base resources and this command must not claim to have started them.
func splitByPresence(wsName, namespace, stackName string, want []string, present map[string]bool) (here, inBase []string) {
	for _, svc := range want {
		if stackName != "" && !present[resourceName(wsName, svc, namespace)] {
			inBase = append(inBase, svc)
			continue
		}
		here = append(here, svc)
	}
	return here, inBase
}

// autoStartOutcome decides what a failed auto-start means for the service start
// that triggered it.
//
// A daemon that will not come up is fatal: nothing can be triggered. A
// workspace.up hook that failed is not, because the daemon IS up and the
// service can start. Reporting the two the same way told the user the daemon
// had failed when it had not, and abandoned a service start that would have
// worked.
func autoStartOutcome(daemonErr, hookErr error, services []string) (error, []string) {
	if daemonErr != nil {
		return fmt.Errorf("failed to auto-start dev daemon: %w", daemonErr), nil
	}
	if hookErr == nil {
		return nil, nil
	}
	return nil, []string{
		fmt.Sprintf("the dev daemon is up, but a workspace.up hook failed: %v", hookErr),
		fmt.Sprintf("%s starts anyway. Fix the hook, then re-run it: devstack hooks run workspace.up", strings.Join(services, ", ")),
	}
}
