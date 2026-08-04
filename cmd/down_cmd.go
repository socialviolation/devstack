package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop this workspace's services, and the daemon if nothing else needs it",
	Long: `Stop this workspace's active stacks, and remove the workspace from the dev
daemon.

The daemon serves the whole machine. If another workspace is still active,
devstack leaves the daemon running. devstack stops the daemon only when no
workspace is active. devstack treats the shared collector the same way.

To start again, run 'devstack workspace up'.`,
	RunE: runDown,
}

func init() {
	workspaceCmd.AddCommand(downCmd)

	downCmd.Flags().Bool("all", false, "Stop all running workspaces")
}

func runDown(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	if all {
		return runDownAll()
	}

	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}

	fireTeardownHooks(ws, "", config.EventWorkspaceDown, nil)

	deactivated, err := stack.DeactivateAll(ws.Name)
	if err != nil {
		return fmt.Errorf("can not deactivate the stacks of %s: %w", ws.Name, err)
	}
	if len(deactivated) > 0 {
		fmt.Printf("Stopped %d active stack(s) of '%s': %s\n", len(deactivated), ws.Name, strings.Join(deactivated, ", "))
	}

	if err := workspace.SetWorkspaceActive(ws.Name, false); err != nil {
		return fmt.Errorf("can not mark the workspace inactive: %w", err)
	}
	if _, err := regenerateHostTiltfile(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: devstack can not generate the host Tiltfile again: %v\n", err)
	}

	anyActive, err := workspace.AnyWorkspaceActive()
	if err != nil {
		return err
	}

	var hostErr error
	if anyActive {
		fmt.Printf("Removed '%s' from the host daemon. Other workspaces are still active, so the daemon keeps running.\n", ws.Name)
	} else {
		fmt.Printf("No workspace is active, so devstack stops the host daemon.\n")
		hostErr = stopHostDaemon()
	}

	// Stop observability stack
	if isOtelRunning(ws) {
		plugin := activePlugin(ws)
		if err := stopOtelStack(ws, plugin); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: OTEL stop failed: %v\n", err)
		} else {
			fmt.Printf("  ✓ OTEL stopped\n")
		}
	} else {
		fmt.Printf("  OTEL not running\n")
	}

	if composeSpec, err := infra.ResolveComposeSpec(ws.Path); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: compose infra configuration error: %v\n", err)
	} else if composeSpec != nil {
		if err := infra.Down(composeSpec); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: compose infra stop failed: %v\n", err)
		} else {
			fmt.Printf("  ✓ Compose infra stopped\n")
		}
	}

	return hostErr
}

// stopHostDaemon gracefully shuts down the one host Tilt daemon: it disables every
// resource, kills the tracked PID, removes the PID file, and closes the host
// session. It is a no-op (not an error) when the daemon is already stopped.
func stopHostDaemon() error {
	pidFile := workspace.HostPIDFile()
	pidData, pidErr := os.ReadFile(pidFile)
	if pidErr != nil {
		apiURL := fmt.Sprintf("http://localhost:%d/api/view", workspace.HostTiltPort)
		if !isTiltReachable(apiURL) {
			fmt.Printf("  Host daemon is not running\n")
			return nil
		}
		fmt.Fprintf(os.Stderr, "Warning: there is no host PID file, but the daemon answers. A process outside devstack can have started it\n")
		return nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return fmt.Errorf("the host PID file %s holds an invalid PID: %w", pidFile, err)
	}

	fmt.Printf("Stopping host daemon (pid %d)...\n", pid)

	tiltClient := tilt.NewClient("localhost", workspace.HostTiltPort)
	if view, err := tiltClient.GetView(); err == nil {
		for _, r := range view.UiResources {
			if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
				continue
			}
			tiltClient.RunCLI("disable", r.Metadata.Name) //nolint:errcheck
		}
		fmt.Printf("  ✓ Services stopped\n")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: can not find process %d: %v\n", pid, err)
	} else if killErr := proc.Kill(); killErr != nil && isProcessAlive(pid) {
		fmt.Fprintf(os.Stderr, "Warning: can not kill process %d: %v\n", pid, killErr)
	}

	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: can not remove the host PID file: %v\n", err)
	}

	time.Sleep(500 * time.Millisecond)
	ports := []int{workspace.HostTiltPort}
	hostName := workspace.HostWorkspace().Name
	if session, err := workspace.LoadSession(hostName); err == nil && len(session.ActivePorts) > 0 {
		ports = session.ActivePorts
	}
	residue := workspace.DetectResidue(pid, ports)
	if err := workspace.CloseSession(hostName, residue); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: can not update the host session state: %v\n", err)
	}

	fmt.Printf("  ✓ Host daemon stopped\n")
	if len(residue) > 0 {
		return fmt.Errorf("the host daemon stopped, but it left residue: %s", strings.Join(residue, ", "))
	}
	return nil
}

func runDownAll() error {
	workspaces, err := workspace.All()
	if err != nil {
		return err
	}

	for i := range workspaces {
		if deactivated, err := stack.DeactivateAll(workspaces[i].Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to deactivate stacks for %s: %v\n", workspaces[i].Name, err)
		} else if len(deactivated) > 0 {
			fmt.Printf("Stopped %d active stack(s) of '%s': %s\n", len(deactivated), workspaces[i].Name, strings.Join(deactivated, ", "))
		}
		if err := workspace.SetWorkspaceActive(workspaces[i].Name, false); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to mark %s inactive: %v\n", workspaces[i].Name, err)
		}
	}
	if _, err := regenerateHostTiltfile(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to regenerate host Tiltfile: %v\n", err)
	}

	hostErr := stopHostDaemon()

	for i := range workspaces {
		ws := workspaces[i]
		if isOtelRunning(&ws) {
			plugin := activePlugin(&ws)
			if err := stopOtelStack(&ws, plugin); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: OTEL stop failed for %s: %v\n", ws.Name, err)
			} else {
				fmt.Printf("  ✓ OTEL stopped (%s)\n", ws.Name)
			}
		}
	}

	return hostErr
}
