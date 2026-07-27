package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/workspace"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the dev daemon for the current workspace",
	Long: `Start the dev daemon as a detached background process for the current workspace.

The dev daemon is responsible for running all local services, watching source
files for changes, and hot-reloading services when code is modified. It must
be running before you can start, stop, or restart individual services.

The shared observability stack is also started automatically so services can
begin shipping traces and logs immediately.

Logs are written to ~/.local/share/devstack/<workspace-name>/tilt.log.`,
	RunE: runStart,
}

func init() {
	workspaceCmd.AddCommand(upCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	if !config.HasWorkspaceManifest(ws.Path) {
		return fmt.Errorf("no %s in %s — this workspace isn't manifest-based yet", config.WorkspaceManifestFileName, ws.Path)
	}

	if err := workspace.SetWorkspaceActive(ws.Name, true); err != nil {
		return fmt.Errorf("failed to mark workspace active: %w", err)
	}
	if _, err := regenerateHostTiltfile(); err != nil {
		return fmt.Errorf("failed to generate host Tiltfile: %w", err)
	}
	if err := ensureHostDaemon(); err != nil {
		return err
	}
	fmt.Printf("Service(s) for '%s' run in the host daemon on :%d as %s:<svc>.\n", ws.Name, workspace.HostTiltPort, ws.Name)

	if composeSpec, err := infra.ResolveComposeSpec(ws.Path); err != nil {
		fmt.Fprintf(os.Stderr, "compose infra config error: %v\n", err)
	} else if composeSpec != nil {
		fmt.Printf("Starting compose infra...\n")
		if err := infra.Up(composeSpec); err != nil {
			fmt.Fprintf(os.Stderr, "compose infra failed: %v\n", err)
		} else {
			fmt.Printf("✓ Compose infra started\n")
		}
	}

	// 10. Start observability backend — only when the workspace opts in.
	// We don't assume services are OTEL-instrumented; enable it in the workspace
	// manifest (observability.enabled) to run a collector and ship telemetry.
	// A feature stack never runs its own collector — it attaches to the base's
	// (generation points its OTEL endpoint there), and two collectors cannot bind
	// the same host ports anyway.
	if !config.ObservabilityEnabled(ws.Path) {
		fmt.Printf("Observability disabled for this workspace — skipping collector.\n")
		fmt.Printf("  Enable it: set observability.enabled: true in %s, then: devstack otel start\n", config.WorkspaceManifestFileName)
	} else if isOtelRunning(ws) {
		fmt.Printf("OTEL stack already running\n")
	} else {
		plugin := activePlugin(ws)
		if plugin == nil {
			fmt.Fprintf(os.Stderr, "No OTEL plugin configured\n")
		} else {
			fmt.Printf("Starting OTEL stack (plugin: %s)...\n", plugin.Name())
			if err := startOtelStack(ws, plugin); err != nil {
				fmt.Fprintf(os.Stderr, "OTEL stack failed: %v\n", err)
			} else {
				queryEndpoint := plugin.QueryEndpoint(ws)
				if queryEndpoint != "" {
					fmt.Printf("✓ OTEL %s\n", queryEndpoint)
				} else {
					fmt.Printf("✓ OTEL collector running (plugin: %s)\n", plugin.Name())
				}
			}
		}
	}

	return nil
}

// ensureHostDaemon starts the one host Tilt daemon if it is not already running,
// printing the status line hostdaemon.EnsureDaemon reports.
func ensureHostDaemon() error {
	msg, err := hostdaemon.EnsureDaemon()
	if err != nil {
		return err
	}
	if msg != "" {
		fmt.Println(msg)
	}
	return nil
}

// resolveWorkspace resolves a workspace by name/path flag or auto-detects from cwd.
func resolveWorkspace(flag string) (*workspace.Workspace, error) {
	if flag == "" {
		return workspace.DetectFromCwd()
	}

	// Try by name first, then by path
	ws, err := workspace.FindByName(flag)
	if err == nil {
		return ws, nil
	}

	ws, err = workspace.FindByPath(flag)
	if err == nil {
		return ws, nil
	}

	return nil, fmt.Errorf("workspace %q not found by name or path", flag)
}

// isTiltReachable returns true if the Tilt API is responding at the given URL.
func isTiltReachable(url string) bool {
	return hostdaemon.TiltReachable(url)
}

// isProcessAlive returns true if a process with the given PID exists and is running.
func isProcessAlive(pid int) bool {
	return hostdaemon.ProcessAlive(pid)
}
