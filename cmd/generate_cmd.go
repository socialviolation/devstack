package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/hostdaemon"
)

// workspaceGenerateCmd manually refreshes the host daemon's Tiltfile. It normally
// runs automatically as part of `devstack workspace up`, so this is only for
// inspecting the artifact without starting the daemon.
var workspaceGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Regenerate the host daemon's Tiltfile from devstack manifests",
	Long: `Regenerate the host daemon's Tiltfile. That is the one file that the running
Tilt daemon reads. It holds the base services of every active workspace, and the
overlay services of every active feature stack.

The Tiltfile is a build artifact. Edit the manifests, not the Tiltfile.

'devstack workspace up' runs this command for you.`,
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
// alias for the many cmd call sites. Generation warnings go to stderr here, so
// every one of those sites reports them without repeating the code.
func regenerateHostTiltfile() (string, error) {
	path, warnings, err := hostdaemon.Regenerate()
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	return path, err
}
