package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
)

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Run a task that a service declares, in a stack's worktree",
}

var taskRunCmd = &cobra.Command{
	Use:   "run <service> [task] --stack <name>",
	Short: "Run a declared task of a service in a stack's worktree. Without a task, list the tasks",
	Long: `Run a task that a service declares under tasks: in its service manifest.

The task runs under sh -c in the stack's worktree of that service. A task's
workDir is relative to that worktree. Its run and env take the same
references as a hook, and they resolve to the stack's own ports.

A task stops at its timeout (10m by default). devstack then kills its whole
process group. devstack prints the exit code, the time it took, and the last
64KB of its output. If the task fails, the command exits non-zero.

Without a task name, devstack lists the tasks that the service declares.

  devstack task run api --stack feat
  devstack task run api test --stack feat`,
	Args:         cobra.RangeArgs(1, 2),
	SilenceUsage: true,
	RunE:         runTaskRun,
}

func init() {
	rootCmd.AddCommand(taskCmd)
	taskCmd.AddCommand(taskRunCmd)
	taskRunCmd.Flags().String("stack", "", "The stack whose worktree runs the task")
	_ = taskRunCmd.MarkFlagRequired("stack")
}

func runTaskRun(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	stackName, _ := cmd.Flags().GetString("stack")
	task := ""
	if len(args) > 1 {
		task = args[1]
	}
	out, failed, err := nvxmcp.RunTask(cmd.Context(), base, stackName, args[0], task,
		"--stack <name>", fmt.Sprintf("run: devstack stack add %s %s", stackName, args[0]))
	if err != nil {
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), out)
	if failed {
		return errors.New("the task failed")
	}
	return nil
}
