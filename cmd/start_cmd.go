package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Start this workspace's services in the dev daemon",
	Long: `Fold this workspace's services into the dev daemon, starting the daemon as a
detached background process if it is not already up.

One daemon serves the whole machine. It runs every workspace's services, watches
their source files, and hot-reloads them when code changes. 'devstack service start' also
brings it up on demand, so you rarely need this command first.

The shared observability stack is also started automatically so services can
begin shipping traces and logs immediately.

Logs are written to ~/.local/share/devstack/<workspace-name>/tilt.log.`,
	RunE: runStart,
}

func init() {
	workspaceCmd.AddCommand(upCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	ws, err := bringWorkspaceUp()
	if err != nil {
		return err
	}
	return fireHooks(ws, "", config.EventWorkspaceUp, nil)
}

// bringWorkspaceUp does everything 'workspace up' does except fire the
// workspace.up hooks, and returns the workspace it acted on.
//
// The two are separated because a caller that only wanted the daemon must be
// able to tell a daemon failure from a hook failure. Reporting a broken hook as
// "failed to auto-start dev daemon" names a problem that does not exist, and
// abandons a service start whose daemon is up and waiting.
func bringWorkspaceUp() (*workspace.Workspace, error) {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return nil, err
	}
	if !config.HasWorkspaceManifest(ws.Path) {
		return nil, fmt.Errorf("no %s in %s — this workspace is not manifest-based yet", config.WorkspaceManifestFileName, ws.Path)
	}

	if err := workspace.SetWorkspaceActive(ws.Name, true); err != nil {
		return nil, fmt.Errorf("failed to mark workspace active: %w", err)
	}
	if err := ensureReplica(ws); err != nil {
		return nil, err
	}
	if _, err := regenerateHostTiltfile(); err != nil {
		return nil, fmt.Errorf("failed to generate host Tiltfile: %w", err)
	}
	if err := ensureHostDaemon(); err != nil {
		return nil, err
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
	// We do not assume services are OTEL-instrumented; enable it in the workspace
	// manifest (observability.enabled) to run a collector and ship telemetry.
	// A feature stack never runs its own collector — it attaches to the base's
	// (generation points its OTEL endpoint there), and two collectors cannot bind
	// the same host ports anyway.
	if !config.ObservabilityEnabled(ws.Path) {
		fmt.Printf("Observability disabled for this workspace — skipping collector.\n")
		fmt.Printf("  Turn it on: devstack otel config on (writes %s), then: devstack otel start\n", config.WorkspaceManifestFileName)
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

	return ws, nil
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
//
// Neither a stack worktree nor the replica is a registered workspace, so the
// registry alone cannot place someone standing in one — and both are places
// commands legitimately run. Which workspace this is says nothing about which of
// its instances a command acts on: stack.ResolveTarget decides that.
func resolveWorkspace(flag string) (*workspace.Workspace, error) {
	if flag == "" {
		ws, err := workspace.DetectFromCwd()
		if err == nil {
			return ws, nil
		}
		if base, _, derr := stack.DetectFromCwd(); derr == nil && base != nil {
			return base, nil
		}
		if base, derr := replica.DetectFromCwd(); derr == nil && base != nil {
			return base, nil
		}
		return nil, err
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

// isProcessAlive returns true if a process with the given PID exists and runs.
func isProcessAlive(pid int) bool {
	return hostdaemon.ProcessAlive(pid)
}
