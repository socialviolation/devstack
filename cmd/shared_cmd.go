package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

var sharedCmd = &cobra.Command{
	Use:   "shared",
	Short: "Inspect and refresh the shared services of a workspace",
	Long: `A shared service is a service in the workspace "shared:" list. It runs once
for the workspace, and never in a stack. Every stack uses it. Databases and
stores are shared.

A shared service runs from the replica: one git worktree for each repository
that holds a shared service, detached at the default branch tip, under a
.devstack-base sibling of the workspace. Nothing runs in your checkout.

SUBCOMMANDS
  devstack shared status        print each shared service and its replica commit
  devstack shared sync          move each replica worktree to the default branch
                                tip, and restart the shared services that moved
  devstack shared path [svc]    print the replica root, or one shared service's
                                replica worktree

These commands have one MCP tool: 'shared'. An agent can use only action="path".`,
	SilenceUsage: true,
	RunE:         runSharedStatus,
}

var sharedStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Print each shared service, its replica worktree and its commit",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runSharedStatus,
}

var sharedSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Move the replica to the default branch tip, and restart the shared services that moved",
	Long: `If the replica does not exist, devstack builds it first.

For each repository that holds a shared service, devstack fetches it. It then moves the
replica worktree to the default branch tip. If the commit of a repository
changed, devstack restarts the shared services in that repository that run now.
It starts no shared service that is stopped.

If a fetch fails, devstack keeps the current commit and writes a warning. A
failed fetch never stops a shared service.

If the workspace has no shared services, this command does nothing.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runSharedSync,
}

var sharedPathCmd = &cobra.Command{
	Use:          "path [service]",
	Short:        "Print the replica root, or one shared service's replica worktree",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runSharedPath,
}

var baseCmd = &cobra.Command{
	Use:          "base",
	Hidden:       true,
	Short:        "Old name of 'devstack shared'",
	SilenceUsage: true,
	RunE:         deprecatedBase(runSharedPath),
}

var baseSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Old name of 'devstack shared sync'",
	SilenceUsage: true,
	RunE:         deprecatedBase(runSharedSync),
}

var basePathCmd = &cobra.Command{
	Use:          "path [service]",
	Short:        "Old name of 'devstack shared path'",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         deprecatedBase(runSharedPath),
}

func init() {
	rootCmd.AddCommand(sharedCmd)
	sharedCmd.AddCommand(sharedStatusCmd)
	sharedCmd.AddCommand(sharedSyncCmd)
	sharedCmd.AddCommand(sharedPathCmd)
	rootCmd.AddCommand(baseCmd)
	baseCmd.AddCommand(baseSyncCmd)
	baseCmd.AddCommand(basePathCmd)
}

func deprecatedBase(run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(os.Stderr, "warning: 'devstack base' is deprecated. Use 'devstack shared'.")
		return run(cmd, args)
	}
}

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

func runSharedSync(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	if !config.HasWorkspaceManifest(replica.Root(ws)) {
		if err := buildBase(ws); err != nil {
			return err
		}
	}
	return syncShared(ws, os.Stdout)
}

var sharedRestarter = hostdaemon.SharedRestarter

func syncShared(ws *workspace.Workspace, w io.Writer) error {
	return hostdaemon.SyncShared(w, ws, sharedRestarter(w, ws.Name))
}

func runSharedStatus(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return err
	}
	names := template.SharedNames()
	if len(names) == 0 {
		fmt.Printf("Workspace %q has no shared services. To add one, list it under shared: in %s.\n", ws.Name, config.WorkspaceManifestFileName)
		return nil
	}
	rw, err := replica.Resolve(ws)
	if err != nil {
		return err
	}
	fmt.Printf("Replica for %q: %s\n", ws.Name, replica.Root(ws))
	for _, n := range names {
		svc, ok := rw.Services[n]
		if !ok {
			fmt.Printf("  %-16s not in the replica. To build it, run: devstack shared sync\n", n)
			continue
		}
		fmt.Printf("  %-16s %-24s %s\n", n, gitinfo.Read(svc.RepoPath).Label(), svc.RepoPath)
	}
	return nil
}

func runSharedPath(cmd *cobra.Command, args []string) error {
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
	if !ok || !rw.IsShared(args[0]) {
		return fmt.Errorf("%q is not a shared service of workspace %q. The shared services are: %s", args[0], ws.Name, strings.Join(rw.SharedNames(), ", "))
	}
	fmt.Println(svc.RepoPath)
	return nil
}

func startShared(w io.Writer, ws *workspace.Workspace) error {
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return err
	}
	names := template.SharedNames()
	if len(names) == 0 {
		return nil
	}
	client := tilt.NewClient("localhost", workspace.HostTiltPort)
	for _, note := range hostdaemon.SyncAndReload(client, hostdaemon.ScopeAll()) {
		fmt.Fprintln(w, note)
	}
	view, err := client.GetView()
	if err != nil {
		return fmt.Errorf("devstack can not reach the host daemon, so it starts no shared service: %w", err)
	}
	byName := map[string]tilt.UIResource{}
	for _, r := range view.UiResources {
		byName[r.Metadata.Name] = r
	}
	var started []string
	for _, n := range names {
		rn := resourceName(ws.Name, n, "")
		r, ok := byName[rn]
		if !ok || isRunningCopy(r) {
			continue
		}
		if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
			if out, err := client.RunCLI("enable", rn); err != nil {
				return fmt.Errorf("devstack can not enable %s: %v\n%s", rn, err, out)
			}
		}
		if out, err := client.RunCLI("trigger", rn); err != nil {
			return fmt.Errorf("devstack can not start %s: %v\n%s", rn, err, out)
		}
		started = append(started, n)
	}
	if len(started) > 0 {
		fmt.Fprintf(w, "✓ Started the shared services: %s\n", strings.Join(started, ", "))
	}
	return nil
}

func requireSharedWithoutStack(ws *workspace.Workspace, namespace string, services []string) error {
	if namespace != "" {
		return nil
	}
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return err
	}
	return stack.RequireShared(rw, services, "--stack <name>")
}
