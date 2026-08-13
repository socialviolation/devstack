package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/panel"
)

var panelCmd = &cobra.Command{
	Use:   "panel",
	Short: "Watch every service, and open the address of one",
	Long: `Watch what runs on this machine, and act on one service.

The panel shows the workspace of the directory it opens in. Below the
workspace, it shows base and each feature stack. From a directory outside every
workspace, the panel shows all the workspaces. To name a workspace yourself,
pass --workspace. Each service shows its state, its port, and the tailnet
address that reaches it. The panel reads the machine again every few seconds.

Move with the arrow keys. Press ? for the full list of keys.

  enter  open the address in the browser
  y      copy the address
  s      start the service
  r      restart the service
  x      stop the service
  l      read the process log

Press O for the address picker. Type a few letters of a service, a stack or a
port. Then press enter to open that address. 'devstack panel --jump' opens the
picker at once. When you pick an address, the panel closes.

The 'machine' group holds the host daemon and the collector. One of each serves
every workspace. A 'containers' group holds the docker containers of one
workspace. The panel does not start or stop these two groups.

The panel is the pane of the herdr plugin. It also runs in any terminal.`,
	RunE: runPanel,
}

func init() {
	rootCmd.AddCommand(panelCmd)
	panelCmd.Flags().Bool("jump", false, "Open the address picker, and close the panel when you pick an address")
	panelCmd.Flags().Bool("launch-decision", false, "Read a herdr pane list on stdin, and report where to open the panel")
	panelCmd.Flags().Bool("launch-cwd", false, "Read a herdr pane list on stdin, and report the directory to open the panel in")
	panelCmd.Flags().Bool("tab", false, "With --launch-decision, look for the panel across the tabs of the workspace")
	_ = panelCmd.Flags().MarkHidden("launch-decision")
	_ = panelCmd.Flags().MarkHidden("launch-cwd")
	_ = panelCmd.Flags().MarkHidden("tab")
}

func runPanel(cmd *cobra.Command, args []string) error {
	if decide, _ := cmd.Flags().GetBool("launch-decision"); decide {
		inTab, _ := cmd.Flags().GetBool("tab")
		return panel.RunLaunchDecision(inTab)
	}
	if where, _ := cmd.Flags().GetBool("launch-cwd"); where {
		return panel.RunLaunchCwd()
	}

	jump, _ := cmd.Flags().GetBool("jump")
	opts := panel.Options{Jump: jump}
	if ws, err := resolveWorkspace(viper.GetString("workspace")); err == nil {
		opts.Workspace = ws.Name
	}
	return panel.Run(opts)
}
