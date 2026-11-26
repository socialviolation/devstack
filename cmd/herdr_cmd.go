package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/panel"
)

var herdrCmd = &cobra.Command{
	Use:   "herdr",
	Short: "Show each stack in the herdr pane that controls it",
	Long: `A herdr pane binds the stacks that a command in it created, brought up,
resynced, adopted or extended. devstack sets the tokens stack, stack_state, stack_url and
stack_stop on that pane. The herdr sidebar shows them.

If herdr is not installed or does not run, these commands do nothing.`,
}

var herdrPublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Set the stack tokens on every herdr pane that binds a stack",
	Long: `Set the stack tokens on every herdr pane that binds a stack. devstack clears
its tokens from a pane that binds no stack. If herdr no longer has a pane,
devstack removes the binding.

devstack runs this after each stack change. The herdr plugin runs it when herdr
starts, because herdr loses the tokens when it restarts.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return herdr.Publish(cmd.Context())
	},
}

var herdrOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the tailnet address of this herdr pane's stack in the browser",
	Long: `Open the tailnet address of the stack that this herdr pane binds. The herdr
plugin action "devstack: open this pane's stack" runs this command.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		pane := herdr.ContextPane()
		if pane == "" {
			return errors.New("devstack can not find the herdr pane. Run this command in a herdr pane")
		}
		url, err := herdr.PaneURL(cmd.Context(), pane)
		if err != nil {
			return err
		}
		fmt.Println(url)
		return panel.OpenURL(url)
	},
}

func init() {
	rootCmd.AddCommand(herdrCmd)
	herdrCmd.AddCommand(herdrPublishCmd)
	herdrCmd.AddCommand(herdrOpenCmd)
}
