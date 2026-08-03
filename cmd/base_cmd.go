package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

var baseCmd = &cobra.Command{
	Use:   "base",
	Short: "Inspect the replica the base workspace runs from",
	Long: `"base" is the workspace running without any stack — and it does not run out of
your checkouts. devstack keeps a replica: one git worktree per service, detached
at that service's default branch tip, under a .devstack-base sibling of the
workspace. Your checkout is the template it is built from — the git objects, the
workspace manifest, the machine-local gitignored config — and nothing runs there,
so work parked half-finished in it neither runs nor blocks.

'devstack workspace up' builds the replica and keeps it in step with the
manifest.

SUBCOMMANDS
  devstack base sync    move every service's worktree to its default branch tip
  devstack base path    print the replica root, or one service's worktree`,
	SilenceUsage: true,
	RunE:         runBasePath,
}

var baseSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Move every replica worktree to its service's default branch tip",
	Long: `Fetch each service and move its replica worktree to the default branch tip,
then refresh the machine-local config copied out of the template.

A fetch that fails is a warning, not an error: being offline leaves the worktree
on the ref it already has, and base keeps running.`,
	SilenceUsage: true,
	RunE:         runBaseSync,
}

var basePathCmd = &cobra.Command{
	Use:          "path [service]",
	Short:        "Print the replica root, or one service's replica worktree",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runBasePath,
}

func init() {
	rootCmd.AddCommand(baseCmd)
	baseCmd.AddCommand(baseSyncCmd)
	baseCmd.AddCommand(basePathCmd)
}

// ensureReplica brings the replica in line with the manifest and reports what
// changed, staying silent when nothing did.
func ensureReplica(ws *workspace.Workspace) error {
	res, err := replica.Ensure(ws)
	if err != nil {
		return err
	}
	for _, wt := range res.Created {
		fmt.Printf("  ✓ replica worktree %-16s %s (%s)\n", wt.Service, wt.Path, wt.Branch)
	}
	for _, name := range res.Removed {
		fmt.Printf("  ✓ removed replica worktree %s — no longer in the manifest\n", name)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return nil
}

func runBaseSync(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	res, err := replica.Sync(ws)
	if err != nil {
		return err
	}

	moved := 0
	for _, s := range res.Services {
		if s.Before != s.After {
			moved++
		}
	}

	fmt.Printf("Replica for %q: %s\n", ws.Name, res.Root)
	for _, s := range res.Services {
		if s.Before == s.After {
			fmt.Printf("  %-16s %-24s %s\n", s.Service, s.Ref, s.After)
			continue
		}
		fmt.Printf("  %-16s %-24s %s → %s\n", s.Service, s.Ref, s.Before, s.After)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if moved == 0 {
		fmt.Println("Nothing moved — every service was already at its default branch tip.")
	}
	return nil
}

func runBasePath(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Println(replica.Root(ws))
		return nil
	}

	rw, err := replica.Resolve(ws)
	if err != nil {
		return err
	}
	svc, ok := rw.Services[args[0]]
	if !ok {
		names := make([]string, 0, len(rw.Services))
		for n := range rw.Services {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf("service %q is not in workspace %q; services: %s", args[0], ws.Name, strings.Join(names, ", "))
	}
	fmt.Println(svc.RepoPath)
	return nil
}
