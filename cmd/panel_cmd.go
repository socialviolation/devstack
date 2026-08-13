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

The panel lists every workspace, base, and each feature stack below it. Each
service shows its state, its port, and the tailnet address that reaches it.
The panel reads the machine again every few seconds.

Move with the arrow keys. Press enter to open the address in the browser, and y
to copy it. Press s to start, r to restart, x to stop, and l to read the log.
Press ? for the full list of keys.

Press O for the link picker: type a few letters of a service, a stack or a
port, and press enter to open that address. 'devstack panel --jump' opens the
picker at once, and closes the panel again when you pick.

The panel is the herdr plugin's pane. It runs in any terminal as well.`,
	RunE: runPanel,
}

func init() {
	rootCmd.AddCommand(panelCmd)
	panelCmd.Flags().Bool("jump", false, "Open the link picker, and close the panel when you pick an address")
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
