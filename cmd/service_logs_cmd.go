package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

var serviceLogsCmd = &cobra.Command{
	Use:   "logs <service>",
	Short: "Print the process output of a service",
	Long: `Print the recent stdout and stderr of one service process.

devstack reads the output from the host daemon. The output starts at the last
restart of the service.

Pass --stack <name> to read the copy in a feature stack. With no flag, devstack
uses the stack whose worktree you are in. Outside a stack worktree, only a
shared service resolves.

If the output is empty, that does not mean that the service is healthy. Run
'devstack status' to see its state.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runServiceLogs,
}

func init() {
	serviceCmd.AddCommand(serviceLogsCmd)
	serviceLogsCmd.Flags().String("stack", "", "The stack to read, by short name. Default: the stack whose worktree you are in. Outside a stack, only a shared service resolves")
	serviceLogsCmd.Flags().Int("lines", 100, "The number of lines to print")
}

func runServiceLogs(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	flagStack, _ := cmd.Flags().GetString("stack")
	stackName, err := stack.ResolveTarget(ws, flagStack)
	if err != nil {
		return err
	}
	lines, _ := cmd.Flags().GetInt("lines")
	if lines < 1 {
		return fmt.Errorf("--lines must be 1 or more, got %d", lines)
	}
	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}
	out, err := nvxmcp.ProcessLogs(ws, tilt.NewClient("localhost", workspace.HostTiltPort), "", cfg, nvxmcp.ProcessLogsRequest{
		Service:      args[0],
		Stack:        stackName,
		Lines:        lines,
		SinceRestart: true,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), out)
	return nil
}
