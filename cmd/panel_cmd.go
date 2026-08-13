package cmd

import (
	"fmt"
	"strings"

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

  enter  take the address: open it, or copy it
  o      open the address in the browser
  y      copy the address
  s      start the service
  r      restart the service
  x      stop the service
  l      read the process log

Press O for the address picker. Type a few letters of a service, a stack or a
port. Then press enter to take that address. In the picker, ctrl+o opens an
address and ctrl+y copies it. 'devstack panel --jump' opens the picker at once.
When you pick an address, the panel closes.

WHAT ENTER DOES
enter decides for itself, and you configure nothing. enter opens the address in
a browser only where that browser opens on your own screen. Everywhere else it
copies the address, because a browser window on another machine helps nobody.

enter copies over ssh, on a machine with no display server, and in a herdr pane.
A herdr pane holds no answer: the herdr server owns the pane, and its client
attaches from this machine or from another one, so the pane can not tell where
you sit.

A copy goes to your terminal, and the terminal puts it on the clipboard of the
machine you sit at. This works over ssh. Your terminal must support OSC 52, and
tmux needs 'set -g set-clipboard on'.

To decide yourself, pass --enter open or --enter copy. To keep that choice, put
"panel": {"enter": "copy"} in ~/.devstack/config.json, or set
DEVSTACK_PANEL_ENTER=copy.

The 'machine' group holds the host daemon and the collector. One of each serves
every workspace. A 'containers' group holds the docker containers of one
workspace. The panel does not start or stop these two groups.

The panel is the pane of the herdr plugin. It also runs in any terminal.`,
	RunE: runPanel,
}

func init() {
	rootCmd.AddCommand(panelCmd)
	panelCmd.Flags().Bool("jump", false, "Open the address picker, and close the panel when you pick an address")
	panelCmd.Flags().String("enter", "", "What the enter key does with an address: auto (default), open or copy. auto copies over ssh, and opens here")
	_ = viper.BindPFlag("panel.enter", panelCmd.Flags().Lookup("enter"))
	_ = viper.BindEnv("panel.enter", "DEVSTACK_PANEL_ENTER")
	panelCmd.Flags().Bool("launch-decision", false, "Read a herdr pane list on stdin, and report where to open the panel")
	panelCmd.Flags().Bool("launch-cwd", false, "Read a herdr pane list on stdin, and report the directory to open the panel in")
	panelCmd.Flags().Bool("tab", false, "With --launch-decision, look for the panel across the tabs of the workspace")
	_ = panelCmd.Flags().MarkHidden("launch-decision")
	_ = panelCmd.Flags().MarkHidden("launch-cwd")
	_ = panelCmd.Flags().MarkHidden("tab")
}

// panelEnterCopies reads the choice of what the enter key does with an address.
// The default decides for itself: it copies where a browser started here would
// open on a screen the reader does not sit at.
func panelEnterCopies(setting string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "", "auto":
		return !panel.BrowserReaches(), nil
	case "open":
		return false, nil
	case "copy":
		return true, nil
	default:
		return false, fmt.Errorf("the enter key takes auto, open or copy, and not %q\nset it for this run:  devstack panel --enter copy\nkeep it:              panel.enter in ~/.devstack/config.json", setting)
	}
}

func runPanel(cmd *cobra.Command, args []string) error {
	if decide, _ := cmd.Flags().GetBool("launch-decision"); decide {
		inTab, _ := cmd.Flags().GetBool("tab")
		return panel.RunLaunchDecision(inTab)
	}
	if where, _ := cmd.Flags().GetBool("launch-cwd"); where {
		return panel.RunLaunchCwd()
	}

	enterCopies, err := panelEnterCopies(viper.GetString("panel.enter"))
	if err != nil {
		return err
	}

	jump, _ := cmd.Flags().GetBool("jump")
	opts := panel.Options{Jump: jump, EnterCopies: enterCopies}
	if ws, err := resolveWorkspace(viper.GetString("workspace")); err == nil {
		opts.Workspace = ws.Name
	}
	return panel.Run(opts)
}
