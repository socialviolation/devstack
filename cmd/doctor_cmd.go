package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/doctor"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Find what is broken in this workspace, and the command that fixes it",
	Long: `Check the running workspace, and name the fix for each problem.

doctor checks the host daemon (down, or still bound to 0.0.0.0), each copy in
error or missing from an up stack, ports a foreign process holds, stacks whose
worktree is gone, stack branches gone from origin or behind it, copies with no
tailnet address, the replica, and orphans: port allocations,
directories and git worktrees that belong to no stack.

It groups the problems as broken, degraded or tidy-up, one line each, with the
fix under it. A healthy workspace gives one line. doctor changes nothing. It
exits non-zero when something is broken.

To check the manifests and the topology instead, run: devstack workspace doctor`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runDoctor,
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove orphaned ports, worktrees and stack records",
	Long: `Remove the orphans of this workspace: port allocations with no stack,
directories and git worktrees under the stacks root with no stack, git worktree
entries whose directory is gone, and the records of stacks whose directory is
gone.

cleanup never touches the replica, a stack whose directory exists, or a stack
that is up. It keeps a worktree that holds uncommitted work, and says so. It ignores
anything younger than 10 minutes, because a stack that is being created looks
like an orphan.

With no flag, cleanup lists what it would do and changes nothing.

  --apply  do it.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runCleanup,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(cleanupCmd)
	doctorCmd.Flags().String("stack", "", "Check one feature stack. Default: the shared set, every stack and the orphans")
	cleanupCmd.Flags().Bool("apply", false, "Remove the orphans. Without it, cleanup only lists them")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	stackName, _ := cmd.Flags().GetString("stack")
	report, err := doctor.Check(ws, stackName, doctor.Live(ws))
	if err != nil {
		return err
	}
	fmt.Print(report.Render(false))
	if n := report.Broken(); n > 0 {
		return fmt.Errorf("doctor found %d broken", n)
	}
	return nil
}

func runCleanup(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	apply, _ := cmd.Flags().GetBool("apply")
	actions, err := doctor.Cleanup(ws, apply, doctor.Live(ws))
	if err != nil {
		return err
	}
	fmt.Print(doctor.RenderCleanup(actions, apply, "To do it, run: devstack cleanup --apply"))
	for _, a := range actions {
		if a.Err != nil {
			return fmt.Errorf("cleanup did not finish: some steps failed")
		}
	}
	return nil
}
