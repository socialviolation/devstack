package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/hostdaemon"
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

// regenerateHostTiltfile delegates to hostdaemon.Regenerate, kept as a package
// alias for the many cmd call sites.
func regenerateHostTiltfile() (string, error) {
	return hostdaemon.Regenerate()
}
