package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
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
  devstack base sync    build the replica if it does not exist, then move every
                        service's worktree to its default branch tip
  devstack base path    print the replica root, or one service's worktree

These commands have one MCP tool: 'base', with action="path" or action="sync".`,
	SilenceUsage: true,
	RunE:         runBasePath,
}

var baseSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Build the replica if it is absent, then move every worktree to its default branch tip",
	Long: `If the replica does not exist, devstack builds it first. It cuts one git
worktree for each repository of the workspace, under a .devstack-base sibling of
the workspace, and it removes a worktree that the manifest no longer lists.

For each service, devstack then fetches the repo and moves the replica worktree
to the default branch tip. devstack then refreshes the machine-local
configuration that it copies out of the template.

This command starts nothing, and it restarts nothing. It does not start the
daemon, and it does not start a service. A copy that runs keeps the old code
until somebody restarts it. To build the replica and run its services, run
'devstack workspace up'.

Each worktree is a new checkout. Before a service starts, its worktree needs its
own dependency install.

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

// buildBase builds the replica of one workspace and prints what it cut. Each
// command that creates something base runs from calls it: 'workspace add',
// 'workspace up' and 'base sync'.
func buildBase(ws *workspace.Workspace) error {
	fmt.Printf("Replica for %q: %s\n", ws.Name, replica.Root(ws))
	_, err := ensureReplica(ws)
	return err
}

func ensureReplica(ws *workspace.Workspace) (*replica.EnsureResult, error) {
	lines, res, err := replicaReport(ws)
	for _, l := range lines {
		fmt.Println(l)
	}
	if err != nil {
		return nil, err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return res, nil
}

// replicaReport builds the replica and returns the report lines it earned, so
// `base sync` can print them and the migration can nest them under its own
// heading.
func replicaReport(ws *workspace.Workspace) ([]string, *replica.EnsureResult, error) {
	res, err := replica.Ensure(ws)
	if err != nil {
		return nil, nil, err
	}
	var lines []string
	for _, wt := range res.Created {
		lines = append(lines, fmt.Sprintf("  ✓ replica worktree %-16s %s (%s)", wt.Repo, wt.Path, wt.Branch))
		if len(wt.Services) > 1 || (len(wt.Services) == 1 && wt.Services[0] != wt.Repo) {
			lines = append(lines, fmt.Sprintf("    ↳ holds the services %s", strings.Join(wt.Services, ", ")))
		}
	}
	for _, name := range res.Removed {
		lines = append(lines, fmt.Sprintf("  ✓ removed replica worktree %s — the manifest no longer lists it", name))
	}
	return lines, res, nil
}

func runBaseSync(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	// A sync of a replica that is not there has nothing to move, and the user
	// wants the same end state either way: the replica at its default branch tip.
	if !config.HasWorkspaceManifest(replica.Root(ws)) {
		fmt.Printf("There is no replica for %q yet, so devstack builds it first.\n", ws.Name)
		if err := buildBase(ws); err != nil {
			return err
		}
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
