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
	Short: "Inspect the replica that the base workspace runs from",
	Long: `"base" is the workspace that runs with no stack, and it does not run out of your
checkouts. devstack keeps a replica: one git worktree for each repository,
detached at that repository's default branch tip, under a .devstack-base sibling
of the workspace. A repository that holds several services gets one worktree,
and each service is a directory in it.

Your checkout is the template that devstack builds the replica from. The
template holds the git objects, the workspace manifest and the machine-local
gitignored configuration. Nothing runs in the template, so half-finished work
there neither runs nor blocks.

'devstack workspace up' builds the replica and keeps it in step with the
manifest.

SUBCOMMANDS
  devstack base         print the replica root (the same as 'base path')
  devstack base sync    move every service's worktree to its default branch tip
  devstack base path    print the replica root, or one service's worktree

These commands have one MCP tool: 'base', with action="path" or action="sync".`,
	SilenceUsage: true,
	RunE:         runBasePath,
}

var baseSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Move every replica worktree to its service's default branch tip",
	Long: `For each service, devstack fetches the repo and moves the replica worktree to
the default branch tip. devstack then refreshes the machine-local configuration
that it copies out of the template.

This command restarts nothing. A copy that runs keeps the old code until
somebody restarts it.

A fetch that fails is a warning, not an error. If the machine is offline, the
worktree stays on the ref that it already has, and base keeps running.`,
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

func ensureReplica(ws *workspace.Workspace) error {
	res, err := replica.Ensure(ws)
	if err != nil {
		return err
	}
	for _, wt := range res.Created {
		fmt.Printf("  ✓ replica worktree %-16s %s (%s)\n", wt.Repo, wt.Path, wt.Branch)
		if len(wt.Services) > 1 || (len(wt.Services) == 1 && wt.Services[0] != wt.Repo) {
			fmt.Printf("    ↳ holds the services %s\n", strings.Join(wt.Services, ", "))
		}
	}
	for _, name := range res.Removed {
		fmt.Printf("  ✓ removed replica worktree %s — the manifest no longer lists it\n", name)
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
		fmt.Println("Nothing moved. Every service was already at its default branch tip.")
	}
	return nil
}

func runBasePath(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	rw, err := replica.Resolve(ws)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Println(replica.Root(ws))
		return nil
	}
	svc, ok := rw.Services[args[0]]
	if !ok {
		names := make([]string, 0, len(rw.Services))
		for n := range rw.Services {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf("service %q is not in workspace %q. Its services: %s", args[0], ws.Name, strings.Join(names, ", "))
	}
	fmt.Println(svc.RepoPath)
	return nil
}
