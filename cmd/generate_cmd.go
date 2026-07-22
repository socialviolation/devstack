package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

// workspaceGenerateCmd manually refreshes the host daemon's Tiltfile. It normally
// runs automatically as part of `devstack workspace up`, so this is only for
// inspecting the artifact without starting the daemon.
var workspaceGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Regenerate the host daemon's Tiltfile from devstack manifests",
	Long: `Regenerate the host daemon's Tiltfile — the single file the running Tilt
daemon reads — composing every active workspace's base services plus each
active feature stack's overlay services.

The Tiltfile is a build artifact — edit the manifests, not the Tiltfile.
Runs automatically as part of 'devstack workspace up'.`,
	SilenceUsage: true,
	RunE:         runGenerate,
}

func init() {
	workspaceCmd.AddCommand(workspaceGenerateCmd)
}

func runGenerate(cmd *cobra.Command, args []string) error {
	path, err := regenerateHostTiltfile()
	if err != nil {
		return err
	}
	fmt.Printf("✓ Generated %s\n", path)
	return nil
}

// regenerateTiltfile renders a base workspace's Tiltfile from its manifests plus
// every active feature stack's overlay services (namespaced <svc>:<stack>) and
// writes it to <ws.Path>/Tiltfile. With no active stacks the output is
// byte-identical to base-only generation. Returns the written path.
func regenerateTiltfile(ws *workspace.Workspace) (string, error) {
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace manifests: %w", err)
	}

	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}

	stackGens, err := hostdaemon.ActiveStackGens(ws)
	if err != nil {
		return "", err
	}

	out, err := tiltgen.GenerateCombined(rw, tiltgen.Options{ManagedEnv: workspace.ManagedEnv(ws, names)}, stackGens)
	if err != nil {
		return "", err
	}

	path := filepath.Join(ws.Path, "Tiltfile")
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return "", fmt.Errorf("failed to write Tiltfile: %w", err)
	}
	return path, nil
}

// regenerateHostTiltfile delegates to hostdaemon.Regenerate, kept as a package
// alias for the many cmd call sites.
func regenerateHostTiltfile() (string, error) {
	return hostdaemon.Regenerate()
}
