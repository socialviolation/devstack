package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

var stackCmd = &cobra.Command{
	Use:   "stack",
	Short: "Create and manage feature stacks",
	Long: `A feature stack runs the services that a feature needs, from their own git
worktrees, on their own ports. Its members are the services that you name and
the services that they need. A service that a member only calls does not come
in.

A shared service is a service in the workspace "shared:" list. It runs once for
the workspace, and never in a stack. Every stack uses it. Every other service
runs only in a stack.

The names "shared" and "base" are reserved. No stack can have either name.

A command that starts, stops or writes a service that is not shared takes
--stack <name>. With no --stack, it acts on the stack that your directory is in.

The telemetry queries are the exception. 'devstack otel traces' and
'devstack otel logs' with no --stack search every stack and the shared set.
Pass --stack <name> for one stack. Pass --stack shared for the shared set.

Never edit stacks.json, ports.json or a stack manifest by hand. Use
'devstack stack add' and 'devstack stack remove'.`,
	RunE: runStackList,
}

var stackCreateCmd = &cobra.Command{
	Use:   "create <name> [--repos a,b] [--template t]",
	Short: "Create a feature stack",
	Long: `Create a feature stack. devstack makes a git worktree and allocates a port for
each service that you name. It does the same for each service that they need,
and prints the needs chain that pulled it in. A shared service is never a member.
If a member calls a service that is not in the stack, devstack warns. To add
that service, run 'devstack stack add'.

A named service gets the stack's branch. A service that comes in through needs
is detached at the default branch tip. With --detach, every worktree is
detached, and the stack has no branch. Use it to run the default branch.

A stack starts from what the team shipped. devstack cuts each worktree from that
repo's default branch. Where the repo has a remote, devstack uses origin's copy
of that branch. devstack never cuts from what your checkout has checked out, so
half-finished work in a checkout does not come into the stack. --from names a
different ref: a release branch, a tag, or a commit.

If the branch already exists, devstack attaches to it with the history that it
already has. --from does not apply to a branch that already exists. If the
branch exists on origin, devstack fetches it and the stack tracks it. To work
on a branch that somebody else pushed, pass --branch <b>.

If a checkout holds uncommitted work, the command says so and carries on. It
does the same if the checkout holds commits that the stack's ref does not have.
That is a warning that this stack does not contain that work. It is not an
error.

--template names a recipe from the templates key in the workspace manifest.
The template gives the services and groups, and --repos adds more. If the
template names an environment, the stack uses it, as 'env use --stack' does.
To list the templates, run 'devstack stack templates'.

Use a short lowercase name, such as the ticket key (nvxa-9999). The branch
name is the stack name. For a real branch name, pass --branch <b>.

A new stack is down. Run 'devstack stack up <name>' to start it. The stop time
starts at create. A stack that is down stays down. 'stack up' sets a new stop
time: DEVSTACK_STOP_AFTER after the up (default 24h). To push the stop time
back, run 'devstack stack extend'. You can extend again and again: no limit
applies to the total.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackCreate,
}

var stackAddCmd = &cobra.Command{
	Use:   "add <name> <service|group> [service|group...]",
	Short: "Add services to a stack that already exists",
	Long: `Put another service into a stack, and disturb nothing that is already in it.
The worktrees that the stack has keep their branches and their work. The
services that it runs keep their ports.

Each named service gets a worktree on the stack's branch. The services that it
needs come in too, in the same way that 'stack create' brings them in. A name that
is already in the stack on its branch is reported, and that is not an error. A
named member on a detached HEAD moves to the stack's branch, so you can commit
in it. If you name nothing new at all, that is an error.

The stack stays exactly as up, or as down, as it was. If the stack is up, the
added services become resources in the host daemon, and devstack does not start
them. Start each one with 'devstack service start <service> --stack <name>'.`,
	Args:         cobra.MinimumNArgs(2),
	SilenceUsage: true,
	RunE:         runStackAdd,
}

var stackDropCmd = &cobra.Command{
	Use:   "remove <name> <service> [service...]",
	Short: "Take services out of a stack, and keep the rest of it as it is",
	Long: `Take services out of a stack. devstack removes them from the stack record and
the stack manifest, and releases their ports. The other members keep their
worktrees, their branches and their ports.

The command refuses to remove a service that another member still needs, and
names that member.

The worktrees of the removed services stay on disk. The command prints the git
command that deletes each one. If the stack is up, the host daemon drops
the removed services.

stack.destroy hooks run first for the removed services only, as in 'stack rm'.

To delete the whole stack, use 'devstack stack rm'.`,
	Args:         cobra.MinimumNArgs(2),
	SilenceUsage: true,
	RunE:         runStackDrop,
}

var stackRemoveCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Stop a stack, remove its worktrees, release its ports, and delete its record",
	Long: `Tear a feature stack down. devstack stops its services, deletes its worktrees,
releases its ports, and deletes its record and its stack root.

The branch stays. The commits that you pushed stay. Work that is only in a
worktree does not stay. devstack deletes the worktree, and that work goes with
it.

BEFORE YOU REMOVE A STACK
devstack keeps the branch, and somebody must decide what happens to it. Ask the
user: merge the branch of this stack, or discard it? Never merge it without an
answer. After a merge, delete the branch: git branch -d <branch>

CAUTION: You can not undo this command.

  --force  devstack deletes worktrees that have uncommitted changes, and
           destroys those changes. Without --force, the command refuses and
           names the dirty worktrees. That is your chance to commit them.

If this workspace declares stack.destroy hooks, they run first, while devstack
can still read the ports and the record. A hook failure does not stop the
teardown. A hook failure therefore means that the external cleanup probably did
not happen. You can not run that hook again afterwards: the record that its
${self...} references resolve against is gone. devstack prints the resolved URLs
at the point of failure. Keep them.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackRemove,
}

var stackListCmd = &cobra.Command{
	Use:          "list",
	Aliases:      []string{"ls"},
	Short:        "List the feature stacks of this workspace",
	SilenceUsage: true,
	RunE:         runStackList,
}

var stackConfigCmd = &cobra.Command{
	Use:   "config <service>",
	Short: "Show the configuration a service runs with, in a stack or in the shared set (read-only)",
	Long: `Show the configuration that devstack gives a service: the effective service
configuration, and the environment ladder that the process receives.

This command reads files. It does not ask the daemon what runs. If the service
is down, the command says so, and it prints the configuration that the service
starts with.

  --stack <name>  the stack's worktree of the service
  no --stack      the stack that holds the current directory. In a plain
                  checkout, a shared service only. devstack reads it from the
                  replica that the shared set runs from.

A service that is not shared runs only in a stack, so it needs --stack. If
devstack has built no replica yet, run 'devstack workspace up'.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackConfig,
}

var stackUpCmd = &cobra.Command{
	Use:   "up <name>",
	Short: "Start a feature stack's services on their own ports, in the one host daemon",
	Long: `Start a feature stack's services on their own ports, in the one host daemon.
If the replica does not exist, devstack builds it. devstack also starts the
shared set.

The stack stops DEVSTACK_STOP_AFTER from now (default 24h). A later 'stack up'
sets a new stop time. To push it back, run 'devstack stack extend'.

devstack checks each worktree against origin. If one is behind, the command
says so and names 'devstack stack resync'. It does not resync.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackUp,
}

var stackStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show a feature stack's services as they run in the host daemon",
	Long: `Show the services in one stack: the state, the ports and the env of each
one. devstack reads them from the one host daemon, and prints them
without the namespace prefix.

'devstack status' is the workspace-level view. It takes --stack for this same
report.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackStatusCmd,
}

var stackDownCmd = &cobra.Command{
	Use:          "down <name>",
	Short:        "Stop a feature stack's services in the host daemon, and keep its worktrees and record",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackDown,
}

var stackSweepCmd = &cobra.Command{
	Use:   "sweep",
	Short: "Stop every stack that is up and past its stop time",
	Long: `Stop every stack that is up and past its stop time, in every workspace of
this machine. A stop is the same as 'devstack stack down': stack.down hooks
fire, and the host daemon drops the stack. devstack keeps the worktrees, the
ports and the record. The sweep never removes a stack.

Every stack gets a stop time when you create it or bring it up: that time plus
DEVSTACK_STOP_AFTER (default 24h). 'devstack stack extend' pushes the stop
time back.

A systemd user timer runs 'devstack tick' every 10 minutes. Each tick runs the
sweep. Once an hour, the tick also runs 'devstack shared sync'.
'devstack workspace up' installs the timer. On a host without systemd, devstack
runs the sweep before a command, at most once in 10 minutes.

  --dry-run  print the stacks that are up and past their stop time, and stop none.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runStackSweep,
}

var stackGCCmd = &cobra.Command{
	Use:          "gc",
	Hidden:       true,
	Short:        "Old name of 'stack sweep'",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(os.Stderr, "Warning: 'devstack stack gc' is deprecated. Use 'devstack stack sweep'.")
		return runStackSweep(cmd, args)
	},
}

func runStackSweep(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	_, err := nvxmcp.Sweep(dryRun, os.Stdout)
	return err
}

var stackExtendCmd = &cobra.Command{
	Use:   "extend <name> <hours>",
	Short: "Push back the stop time of a feature stack",
	Long: fmt.Sprintf(`Push back the stop time of a feature stack. When the stop time passes,
devstack stops the stack, the same way 'devstack stack down' stops it.
devstack never removes it.

%s

'devstack stack up' also sets a new stop time.

Example:
  devstack stack extend perf 8`, wrapText(stack.ExtendRule(), 78)),
	Args:         cobra.ExactArgs(2),
	SilenceUsage: true,
	RunE:         runStackExtend,
}

func runStackExtend(cmd *cobra.Command, args []string) error {
	hours, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		return fmt.Errorf("hours must be a number, got %q", args[1])
	}
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	out, err := nvxmcp.ExtendStack(cmd.Context(), base, args[0], hours)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

var stackResyncCmd = &cobra.Command{
	Use:   "resync <name>",
	Short: "Fast-forward a stack's worktrees to origin, and restart what moved",
	Long: `Move a feature stack to what was pushed. For each repo of the stack that is
on the stack's branch, devstack fetches origin/<branch> and fast-forwards the
worktree to it.

It refuses a repo whose worktree has uncommitted changes, and names the files.
It refuses a repo whose worktree and origin diverged, and names both commits.
It never merges or rebases. For each detached repo, devstack fetches origin's
default branch and fast-forwards the worktree to it, with the same refusals.

If the stack is up, devstack restarts the services of each repo that moved,
and every member that depends on them.

devstack does not add services. It reports each repo outside the stack that
has the stack's branch on origin. It reports each service that a member now
needs and the stack does not run. If the new commits change a service's
migrations directory, devstack says so. A migration changes the shared
database for every stack. Resync runs no migration.

Resync never moves a stack to a different branch. To work on another branch,
create a new stack with --branch <b>.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackResync,
}

func runStackResync(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	herdr.Bind(base.Name, args[0])
	defer herdr.Notify()
	out, err := nvxmcp.ResyncStack(base, args[0], true)
	fmt.Print(out)
	return err
}

var stackAdoptCmd = &cobra.Command{
	Use:   "adopt <name>",
	Short: "Take over an agent's stack, so agents lose access to it",
	Long: `Hand an agent's feature stack to you. devstack clears the stack's owner, so
no agent can act on it. The stop time stays as it is. The record keeps the
agent's name as adopted-from.

devstack prints the stack's worktrees, the branch of each, whether it is up,
and its tailnet addresses. If the stack is already yours, devstack does not
change it.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackAdopt,
}

func runStackAdopt(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	out, err := nvxmcp.AdoptStack(cmd.Context(), base, args[0])
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func init() {
	rootCmd.AddCommand(stackCmd)
	stackCmd.AddCommand(stackSweepCmd)
	stackCmd.AddCommand(stackExtendCmd)
	stackCmd.AddCommand(stackGCCmd)
	stackCmd.AddCommand(stackResyncCmd)
	stackCmd.AddCommand(stackAdoptCmd)
	stackCmd.AddCommand(stackCreateCmd)
	stackCmd.AddCommand(stackAddCmd)
	stackCmd.AddCommand(stackDropCmd)
	stackCmd.AddCommand(stackRemoveCmd)
	stackCmd.AddCommand(stackListCmd)
	stackCmd.AddCommand(stackNoteCmd)
	stackCmd.AddCommand(stackConfigCmd)
	stackCmd.AddCommand(stackUpCmd)
	stackCmd.AddCommand(stackDownCmd)
	stackCmd.AddCommand(stackStatusCmd)
	stackCmd.AddCommand(stackTemplatesCmd)

	stackCreateCmd.Flags().String("repos", "", "Service or group names that this stack runs, separated by commas. A group expands to its members. devstack adds the services that they need")
	stackCreateCmd.Flags().String("branch", "", "Branch for the changed repos (default: the stack name). devstack attaches to it if it already exists")
	stackCreateCmd.Flags().Bool("detach", false, "Cut every worktree detached at the default branch tip, with no branch. Use it to run the default branch")
	stackCreateCmd.Flags().String("from", "", "Ref that devstack cuts the worktrees from (default: each repo's default branch, origin's copy of it where there is one). Never what your checkout has checked out")
	stackCreateCmd.Flags().String("template", "", "Template from the workspace manifest. It gives the services, the groups and the environment. --repos adds more services")
	stackCreateCmd.Flags().String("note", "", "What this stack is for: a ticket URL, an issue key, or a sentence. 'devstack stack list' shows it")
	stackAddCmd.Flags().String("from", "", "Ref that devstack cuts the new worktrees from (default: each repo's default branch, origin's copy of it where there is one)")
	stackNoteCmd.Flags().String("add", "", "Append a dated entry that says where the work got to, instead of replacing the note")
	stackSweepCmd.Flags().Bool("dry-run", false, "Print the stacks past their stop time, and stop none")
	stackGCCmd.Flags().Bool("dry-run", false, "Print the stacks past their stop time, and stop none")
	stackRemoveCmd.Flags().Bool("force", false, "Remove worktrees even if they have uncommitted changes. This destroys that work. You can not recover it")
	stackConfigCmd.Flags().String("stack", "", "Which stack to read. Default: the stack that holds the current directory. In a plain checkout, devstack reads a shared service from the replica")
}

func runStackCreate(cmd *cobra.Command, args []string) error {
	reposFlag, _ := cmd.Flags().GetString("repos")
	changed := splitCSV(reposFlag)
	templateFlag, _ := cmd.Flags().GetString("template")
	if len(changed) == 0 && templateFlag == "" {
		return fmt.Errorf("--repos or --template is required. Name the service or services that this stack changes")
	}

	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}

	branchFlag, _ := cmd.Flags().GetString("branch")
	fromFlag, _ := cmd.Flags().GetString("from")
	noteFlag, _ := cmd.Flags().GetString("note")
	detachFlag, _ := cmd.Flags().GetBool("detach")
	res, err := stack.Create(stack.CreateInput{Base: base, Name: args[0], Repos: changed, Branch: branchFlag, Detach: detachFlag, From: fromFlag, Note: noteFlag, Template: templateFlag})
	if err != nil {
		return err
	}
	herdr.Bind(base.Name, args[0])
	defer herdr.Notify()

	fmt.Printf("Workspace: %s (%s)\n", res.BaseName, res.BasePath)
	if res.Template != "" {
		fmt.Printf("Template: %s\n", res.Template)
	}
	if res.Env != "" {
		fmt.Printf("Environment: %s\n", res.Env)
	}
	fmt.Printf("Members (the services you named, and the services they need):\n")
	for _, m := range res.Members {
		fmt.Printf("  %-16s %s\n", m.Service, m.Describe())
	}
	fmt.Printf("Stack root: %s\n", res.StackRoot)
	for _, wt := range res.Worktrees {
		fmt.Printf("  ✓ worktree %-16s %s (%s)\n", wt.Service, wt.Path, branchNote(wt))
		if wt.Path != wt.RepoPath {
			fmt.Printf("    ↳ in the worktree of the repository %s at %s\n", wt.Repo, wt.RepoPath)
		}
		if len(wt.Materialized) > 0 {
			fmt.Printf("    ↳ copied %d machine-local configuration file(s): %s\n", len(wt.Materialized), strings.Join(wt.Materialized, ", "))
		}
	}
	fmt.Printf("  ✓ generated %s\n", res.ManifestPath)
	fmt.Printf("  ✓ recorded stack %q (workspace %q, down)\n", res.StackName, res.BaseName)
	fmt.Printf("Allocated service ports (key scheme: service/portKey):\n")
	for _, k := range sortedKeys(res.Ports) {
		fmt.Printf("  %-24s http://localhost:%d\n", k, res.Ports[k])
	}

	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	members := make([]string, 0, len(res.Members))
	for _, m := range res.Members {
		members = append(members, m.Service)
	}
	if err := fireHooks(base, args[0], config.EventStackCreate, members); err != nil {
		return fmt.Errorf("%w\ndevstack created stack %q, but its setup hooks did not finish. Fix the hook. Then run the hooks again:\n  devstack hooks run stack.create --stack %s\nOr discard the stack:\n  devstack stack rm %s", err, res.StackName, args[0], args[0])
	}

	fmt.Printf("\nStack %q ready. Start it: devstack stack up %s\n", res.StackName, args[0])
	return nil
}

func runStackAdd(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	fromFlag, _ := cmd.Flags().GetString("from")
	res, err := stack.Add(stack.AddInput{Base: base, Name: args[0], Members: args[1:], From: fromFlag})
	if err != nil {
		return err
	}

	added := make([]string, 0, len(res.Added))
	for _, m := range res.Added {
		added = append(added, m.Service)
	}
	if len(res.Added) > 0 {
		fmt.Printf("Added to stack %q (branch %s):\n", res.StackName, res.Branch)
		for _, m := range res.Added {
			fmt.Printf("  %-16s %s\n", m.Service, m.Describe())
		}
	}
	for _, wt := range res.Worktrees {
		fmt.Printf("  ✓ worktree %-16s %s (%s)\n", wt.Service, wt.Path, branchNote(wt))
		if wt.Path != wt.RepoPath {
			fmt.Printf("    ↳ in the worktree of the repository %s at %s\n", wt.Repo, wt.RepoPath)
		}
		if len(wt.Materialized) > 0 {
			fmt.Printf("    ↳ copied %d machine-local configuration file(s): %s\n", len(wt.Materialized), strings.Join(wt.Materialized, ", "))
		}
	}
	if len(res.Promoted) > 0 {
		fmt.Printf("Promoted to branch %s (these services were in the stack on a detached HEAD, and you can commit in them now): %s\n",
			res.Branch, strings.Join(res.Promoted, ", "))
	}
	fmt.Printf("  ✓ regenerated %s\n", res.ManifestPath)
	if len(res.Ports) > 0 {
		fmt.Printf("Allocated service ports (the stack's existing ports are unchanged):\n")
		for _, k := range sortedKeys(res.Ports) {
			fmt.Printf("  %-24s http://localhost:%d\n", k, res.Ports[k])
		}
	}
	if len(res.AlreadyPresent) > 0 {
		fmt.Printf("Already in the stack, left alone: %s\n", strings.Join(res.AlreadyPresent, ", "))
	}
	fmt.Printf("Members are now: %s\n", strings.Join(res.Overlay, ", "))
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	if len(added) == 0 {
		fmt.Printf("\nStack %q is unchanged apart from the promotion. Nothing was added, so devstack ran no hooks.\n", res.StackName)
		return nil
	}

	if err := fireHooks(base, args[0], config.EventStackCreate, added); err != nil {
		return fmt.Errorf("%w\ndevstack added %s to stack %q, but its %s hooks did not finish. It is not fully provisioned. Fix the hook. Then run the hooks again:\n  devstack hooks run %s --stack %s --services %s",
			err, strings.Join(added, ", "), res.StackName, config.EventStackCreate, config.EventStackCreate, args[0], strings.Join(added, ","))
	}

	if !res.Active {
		fmt.Printf("\nStack %q is down. Start it: devstack stack up %s\n", res.StackName, args[0])
		return nil
	}

	// The stack stays up: regenerating the Tiltfile adds the new resources and
	// leaves the blocks of the copies already running untouched, so nothing that
	// is serving is stopped or restarted here.
	if _, _, err := regenerateHostTiltfileScope(hostdaemon.ScopeStack(base.Name, res.Namespace)); err != nil {
		return fmt.Errorf("can not generate the host Tiltfile again: %w", err)
	}
	syncHostTiltfile(tilt.NewClient("localhost", workspace.HostTiltPort), hostdaemon.ScopeStack(base.Name, res.Namespace))
	fmt.Printf("  ✓ host Tiltfile now carries %s (not started)\n", strings.Join(added, ", "))

	if err := fireHooks(base, args[0], config.EventStackUp, added); err != nil {
		return fmt.Errorf("%w\ndevstack added %s to the running stack %q, but its %s hooks did not finish. It is not fully provisioned. Fix the hook. Then run the hooks again:\n  devstack hooks run %s --stack %s --services %s",
			err, strings.Join(added, ", "), res.StackName, config.EventStackUp, config.EventStackUp, args[0], strings.Join(added, ","))
	}

	fmt.Printf("\nStart the added service(s): %s\n", strings.Join(startCommands(added, args[0]), "  ·  "))
	return nil
}

func runStackDrop(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	res, err := stack.Drop(stack.DropInput{Base: base, Name: args[0], Services: args[1:], Teardown: func(removed []string) {
		fireTeardownHooks(base, args[0], config.EventStackDestroy, removed)
	}})
	if err != nil {
		return err
	}

	fmt.Printf("Removed from stack %q: %s\n", res.StackName, strings.Join(res.Removed, ", "))
	if len(res.ReleasedPorts) > 0 {
		fmt.Printf("  ✓ released ports %s\n", strings.Join(res.ReleasedPorts, ", "))
	}
	fmt.Printf("  ✓ regenerated %s\n", res.ManifestPath)
	fmt.Printf("Members are now: %s\n", strings.Join(res.Overlay, ", "))
	fmt.Println("devstack kept the worktrees on disk:")
	for _, k := range res.Kept {
		fmt.Printf("  %-16s %s\n", k.Service, k.Path)
		if k.RemoveCommand != "" {
			fmt.Printf("    ↳ to delete it: %s\n", k.RemoveCommand)
		} else {
			fmt.Printf("    ↳ a service still in the stack shares this worktree\n")
		}
	}

	if !res.Active {
		return nil
	}
	if _, _, err := regenerateHostTiltfileScope(hostdaemon.ScopeStack(base.Name, res.Namespace)); err != nil {
		return fmt.Errorf("can not generate the host Tiltfile again: %w", err)
	}
	syncHostTiltfile(tilt.NewClient("localhost", workspace.HostTiltPort), hostdaemon.ScopeStack(base.Name, res.Namespace))
	fmt.Printf("  ✓ host Tiltfile no longer carries %s\n", strings.Join(res.Removed, ", "))
	return nil
}

func branchNote(wt stack.WorktreeResult) string {
	if wt.Branch != "" {
		return "branch " + wt.Branch
	}
	if wt.Ref == "" {
		return "detached"
	}
	return "detached at " + wt.Ref
}

func startCommands(services []string, stackName string) []string {
	out := make([]string, 0, len(services))
	for _, s := range services {
		out = append(out, fmt.Sprintf("devstack service start %s --stack %s", s, stackName))
	}
	return out
}

func runStackRemove(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")

	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}

	// A refusal must come before the hooks, not after. stack.destroy hooks
	// de-provision state outside this machine, so a removal that then refuses
	// leaves the stack alive and already de-provisioned, and the next attempt
	// fires them a second time.
	if err := stack.CheckRemovable(base, args[0], force); err != nil {
		return err
	}
	defer herdr.Notify()
	rec, err := stack.Resolve(base.Name, args[0])
	if err != nil {
		return err
	}

	// Before anything is taken away: worktrees, ports and the record are all
	// still readable, so a teardown hook can de-provision what create provisioned.
	fireTeardownHooks(base, args[0], config.EventStackDestroy, nil)

	det, err := hostdaemon.DetachStack(base.Name, rec.Name)
	if err != nil {
		return err
	}
	for _, w := range det.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	if det.RegenErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to regenerate host Tiltfile: %v\n", det.RegenErr)
	}
	if note := hostdaemon.KeptNote(det.Kept); note != "" {
		fmt.Println(note)
	}

	res, err := stack.Remove(base, args[0], force)
	if res != nil {
		fmt.Printf("Removing stack %q (workspace %q)\n", res.Name, res.BaseName)
		for _, p := range res.RemovedWorktrees {
			fmt.Printf("  ✓ removed worktree %s\n", p)
		}
		if res.PortsReleased {
			fmt.Printf("  ✓ released allocated ports\n")
		}
		if res.Deregistered {
			fmt.Printf("  ✓ removed record for %q\n", res.Name)
		}
		if res.RootRemoved {
			fmt.Printf("  ✓ removed stack root %s\n", res.StackRoot)
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	}
	if err != nil {
		return err
	}

	fmt.Printf("✓ Stack %q removed.\n", res.Name)
	return nil
}

func runStackList(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	stacks, err := stack.List(base.Name)
	if err != nil {
		return err
	}
	baseGroups := baseGroupMembers(base)
	if len(stacks) == 0 {
		fmt.Printf("No stacks in workspace %q. Create one with: devstack stack create <name> --repos <svc>\n", base.Name)
		return nil
	}

	fmt.Println("A stack that is up registers its services in the one host daemon, as <workspace>:<service>:<stack>.")
	fmt.Printf("%-16s %-8s %-34s %-30s %s\n", "STACK", "STATUS", "SERVICES", "BRANCH", "AGE")
	fmt.Println(strings.Repeat("-", 100))
	for _, s := range stacks {
		fmt.Printf("%-16s %-8s %-34s %-30s %s\n",
			shortStackName(s.Name, s.BaseName), s.Status,
			truncateCell(strings.Join(s.Services, ", "), 34),
			truncateCell(s.Branch, 30), stackAge(s.Created))
		if s.Note != "" {
			color.New(color.Faint).Printf("  %s\n", s.Note)
		}
		if d := s.Deadline(stack.Now()); d != "" {
			color.New(color.Faint).Printf("  %s\n", d)
		}
		if s.AdoptedFrom != "" {
			color.New(color.Faint).Printf("  adopted from %s\n", s.AdoptedFrom)
		}
		if s.Template != "" {
			color.New(color.Faint).Printf("  template %s\n", s.Template)
		}
		for _, cov := range stack.CoverageOf(s.Groups, s.Services, baseGroups) {
			color.New(color.Faint).Printf("  %s\n", cov.Sentence())
		}
		if n := len(s.Log); n > 0 {
			color.New(color.Faint).Printf("  %s ago  %s\n", stackAge(s.Log[n-1].At), s.Log[n-1].Text)
		}
		var links []string
		for _, k := range sortedKeys(s.Ports) {
			links = append(links, fmt.Sprintf("%s=http://localhost:%d", k, s.Ports[k]))
		}
		if len(links) > 0 {
			color.New(color.Faint).Printf("  %s\n", strings.Join(links, "  "))
		}
	}
	fmt.Println()
	color.New(color.Faint).Println("SERVICES are the members: the services this stack runs. It uses the shared set, and runs no other service.")
	color.New(color.Faint).Println("STATUS up means that the stack's copies are in the daemon. It does not mean that they run. Each copy has its own state. Show it with: devstack status --stack <name>")
	color.New(color.Faint).Println("\"covers group g (3/4). x is not in this stack\" is a group that devstack cut this stack to cover. The stack runs 3 of the 4 members. A group action reaches only the members the stack runs.")
	color.New(color.Faint).Println("To replace what a stack is for, run: devstack stack note <name> \"...\"")
	color.New(color.Faint).Println("To add a dated entry on where the work got to, run: devstack stack note <name> --add \"...\"")
	return nil
}

var stackTemplatesCmd = &cobra.Command{
	Use:          "templates",
	Short:        "List the templates of the workspace",
	Long:         "List the templates in the workspace manifest. A template is a recipe for a new stack: services, groups and an optional environment. A template never runs anything. To use one, run 'devstack stack create <name> --template <template>'.",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runStackTemplates,
}

func runStackTemplates(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	m, err := config.LoadWorkspaceManifest(base.Path)
	if err != nil {
		return err
	}
	names := m.TemplateNames()
	if len(names) == 0 {
		fmt.Printf("Workspace %q has no templates. Add them under templates in %s\n", base.Name, config.WorkspaceManifestFileName)
		return nil
	}
	for _, n := range names {
		t := m.Templates[n]
		fmt.Printf("%s\n", n)
		if t.Purpose != "" {
			fmt.Printf("  %s\n", t.Purpose)
		}
		fmt.Printf("  services: %s\n", strings.Join(t.Services, ", "))
		if t.Env != "" {
			fmt.Printf("  env: %s\n", t.Env)
		}
	}
	fmt.Println("\nCreate a stack from one: devstack stack create <name> --template <template>")
	return nil
}

func shortStackName(full, base string) string {
	return strings.TrimPrefix(full, base+"--")
}

func truncateCell(s string, n int) string {
	if s == "" {
		return "-"
	}
	return clipRunes(s, n)
}

// stackAge is how long a stack has been open. A stack nobody has touched in
// weeks is usually one that was finished and never removed.
func stackAge(created time.Time) string {
	if created.IsZero() {
		return "-"
	}
	d := time.Since(created)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

var stackNoteCmd = &cobra.Command{
	Use:   "note <name> [text]",
	Short: "Show or set what a stack is for",
	Long: fmt.Sprintf(`Record what a stack is for, in your own words: a ticket URL, an issue key, or a
sentence. devstack never derives this text. A branch says what changed. A note
says why. A week later, the note is the part that you can not reconstruct.

[text] replaces the purpose. --add appends a dated entry, and keeps the
purpose. Write where the work got to. Then you can pick the work up next week
without a read of the diff. devstack keeps the last %d entries, each one of %d
characters at most. One entry for each step pushes out the entry that is worth
a read.

With no text, this command prints the purpose and its entries. Pass an empty
string to clear both. 'devstack prime' labels these two "purpose" and "latest".

Examples:
  devstack stack note perf "NAV-412 daily value spike"
  devstack stack note perf --add "cache warms on boot now, spike is in the FX join"
  devstack stack note perf`, stack.NoteLogEntries, stack.NoteEntryMax),
	Args:         cobra.RangeArgs(1, 2),
	SilenceUsage: true,
	RunE:         runStackNote,
}

func runStackNote(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	rec, err := stack.FindStack(base.Name, args[0])
	if err != nil {
		return err
	}
	add, _ := cmd.Flags().GetString("add")

	if add != "" {
		if len(args) == 2 {
			return fmt.Errorf("--add appends an entry. [text] replaces the note. Pass one or the other")
		}
		appended, entry, err := stack.AppendNote(base.Name, rec.Name, add)
		if err != nil {
			return err
		}
		if !appended {
			fmt.Printf("Unchanged — the last entry on %q already says that.\n", rec.Name)
			return nil
		}
		fmt.Printf("✓ %s: %s\n", rec.Name, entry.Text)
		return nil
	}

	if len(args) == 1 {
		if rec.Note == "" && len(rec.Log) == 0 {
			fmt.Printf("No note on stack %q. Set one with: devstack stack note %s \"...\"\n", rec.Name, rec.Name)
			return nil
		}
		if rec.Note != "" {
			fmt.Println(rec.Note)
		}
		for _, e := range rec.Log {
			color.New(color.Faint).Printf("  %s  %s\n", e.At.Format("2006-01-02"), e.Text)
		}
		return nil
	}

	if err := stack.SetNote(base.Name, rec.Name, args[1]); err != nil {
		return err
	}
	if args[1] == "" {
		fmt.Printf("✓ Cleared the note on %q.\n", rec.Name)
		return nil
	}
	fmt.Printf("✓ %s: %s\n", rec.Name, args[1])
	return nil
}

func runStackConfig(cmd *cobra.Command, args []string) error {
	service := args[0]
	stackName, _ := cmd.Flags().GetString("stack")

	var rec *stack.Record
	var err error
	if stackName != "" {
		base, berr := resolveWorkspace(viper.GetString("workspace"))
		if berr != nil {
			return berr
		}
		rec, err = stack.Resolve(base.Name, stackName)
	} else {
		_, rec, err = stack.DetectFromCwd()
		if err != nil || rec == nil {
			return runSharedConfig(service)
		}
	}
	if err != nil {
		return err
	}

	rw, err := stack.ResolveWorktree(rec)
	if err != nil {
		return err
	}
	svc, ok := rw.Services[service]
	if !ok {
		return fmt.Errorf("service %q is not in stack %q. Its services: %s", service, rec.FullName(), strings.Join(sortedServiceNames(rw), ", "))
	}

	entries, err := svcconfig.EffectiveConfig(svc, rec.RuntimeKey())
	if err != nil {
		return err
	}

	tense := "the configuration it runs with"
	if !rec.Active {
		tense = "the configuration it starts with"
		fmt.Printf("Stack %s is down. Nothing runs with this configuration now. To start the stack, run: devstack stack up %s\n", rec.FullName(), rec.Name)
	}
	printConfigTable(service, "stack "+rec.FullName(), tense, entries)
	fmt.Printf("\n* = the stack overrides this value, and devstack computes it. devstack shows a secret value as %s.\n", svcconfig.MaskedValue)

	names := make([]string, 0, len(rw.Services))
	for n := range rw.Services {
		names = append(names, n)
	}
	opts, oerr := stack.GenerateOptions(rec, names)
	var managed map[string]string
	if oerr == nil {
		managed = opts.ManagedEnv[svc.Name]
	}
	layers, lerr := config.EnvLadder(svc.EnvDir(), rw.Manifest, svc.Manifest, rec.Env, managed, svc.ManifestPath)
	if lerr != nil {
		fmt.Printf("\nEnvironment (serve_env ladder): unavailable: %v\n", lerr)
		return nil
	}
	if oerr == nil {
		if rerr := tiltgen.ResolveLayerRefs(layers, svc.Name, opts.Book, opts.Stack); rerr != nil {
			fmt.Printf("\nEnvironment (serve_env ladder): unavailable: %v\n", rerr)
			return nil
		}
	}
	printEnvLadder(layers)
	return nil
}

func runSharedConfig(service string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return err
	}
	if _, ok := template.Services[service]; !ok {
		return fmt.Errorf("service %q is not in workspace %q. Its services: %s", service, ws.Name, strings.Join(sortedServiceNames(template), ", "))
	}
	if err := stack.RequireShared(template, []string{service}, "--stack <name>"); err != nil {
		return err
	}
	rw, err := replica.Resolve(ws)
	if errors.Is(err, replica.ErrNotBuilt) {
		return fmt.Errorf("devstack has not built the replica of workspace %q. The shared set runs from it, so there is no configuration to read yet. To build the replica, run: devstack workspace up", ws.Name)
	}
	if err != nil {
		return fmt.Errorf("the shared set runs from the replica of workspace %q. The replica is stale or incomplete. To rebuild it, run: devstack workspace up. devstack can not read it: %w", ws.Name, err)
	}
	svc, ok := rw.Services[service]
	if !ok {
		return fmt.Errorf("the replica of workspace %q does not hold shared service %q. To rebuild it, run: devstack workspace up", ws.Name, service)
	}

	entries, err := svcconfig.EffectiveConfig(svc, ws.Name)
	if err != nil {
		return err
	}

	tense := "the configuration it runs with"
	if !ws.Active {
		tense = "the configuration it starts with"
		fmt.Printf("The workspace is down, so the shared set does not run. To start it, run: devstack workspace up\n")
	}
	printConfigTable(service, "the shared set", tense, entries)
	fmt.Printf("\ndevstack reads this from the replica at %s. To change it, put the change on the default branch, then run: devstack workspace up\n", svc.RepoPath)
	fmt.Printf("A secret value appears as %s.\n", svcconfig.MaskedValue)

	layers, lerr := resolvedEnvLadder(ws, rw, svc, nil)
	if lerr != nil {
		fmt.Printf("\nEnvironment (serve_env ladder): unavailable: %v\n", lerr)
		return nil
	}
	printEnvLadder(layers)
	return nil
}

func printConfigTable(service, scope, tense string, entries []svcconfig.ConfigEntry) {
	fmt.Printf("Effective configuration for %s in %s (read-only: %s)\n", service, scope, tense)
	fmt.Printf("  %-42s %-12s %s\n", "KEY", "SOURCE", "VALUE")
	fmt.Println(strings.Repeat("-", 90))
	for _, e := range entries {
		marker := "  "
		if e.Overridden {
			marker = "* "
		}
		fmt.Printf("%s%-42s %-12s %s\n", marker, e.Key, e.Source, e.Value)
	}
}

func printEnvLadder(layers []config.EnvLayer) {
	merged := config.MergeEnvLadder(layers)
	source := map[string]config.EnvRung{}
	for _, l := range layers {
		for k := range l.Values {
			source[k] = l.Rung
		}
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("\nEnvironment (serve_env ladder — what the process receives):\n")
	fmt.Printf("  %-42s %-22s %s\n", "KEY", "SOURCE", "VALUE")
	fmt.Println(strings.Repeat("-", 90))
	for _, k := range keys {
		v := svcconfig.RedactValue(k, merged[k])
		fmt.Printf("  %-42s %-22s %s\n", k, string(source[k]), v)
	}
}

func runStackUp(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	rec, err := stack.Resolve(base.Name, args[0])
	if err != nil {
		return err
	}

	if err := workspace.SetWorkspaceActive(base.Name, true); err != nil {
		return fmt.Errorf("can not mark the workspace active: %w", err)
	}
	if err := stack.MarkUp(base.Name, rec.Name); err != nil {
		return err
	}
	herdr.Bind(base.Name, rec.Name)
	defer herdr.Notify()
	if !config.HasWorkspaceManifest(replica.Root(base)) {
		if _, err := ensureReplica(base); err != nil {
			return err
		}
	}
	for _, w := range stack.RecordWarnings(rec) {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	if _, _, err := regenerateHostTiltfileScope(hostdaemon.ScopeStack(base.Name, rec.Name)); err != nil {
		return fmt.Errorf("can not generate the host Tiltfile again: %w", err)
	}
	if err := ensureHostDaemon(); err != nil {
		return err
	}
	if err := hostdaemon.StartShared(os.Stdout, base); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	tiltClient := tilt.NewClient("localhost", workspace.HostTiltPort)
	syncHostTiltfile(tiltClient, hostdaemon.ScopeStack(base.Name, rec.Name))

	started, err := stack.StartServices(tiltClient, base.Name, rec)
	if err != nil {
		return err
	}
	if len(started) == 0 {
		return errors.New(hostdaemon.NoServicesReason(tiltClient, rec.Name))
	}

	fmt.Printf("✓ Stack %q started: %s\n", rec.Name, strings.Join(started, ", "))
	for _, k := range sortedKeys(rec.Ports) {
		fmt.Printf("  %-24s http://localhost:%d\n", stackPortLabel(k), rec.Ports[k])
	}
	behind, notes := stack.BehindWarnings(rec, fmt.Sprintf("Run `devstack stack resync %s`.", rec.Name))
	for _, w := range behind {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	for _, n := range notes {
		fmt.Fprintf(os.Stderr, "note: %s\n", n)
	}

	if err := fireHooks(base, rec.Name, config.EventStackUp, started); err != nil {
		return fmt.Errorf("%w\nStack %q runs, but its setup hooks did not finish. Fix the hook. Then run the hooks again:\n  devstack hooks run stack.up --stack %s", err, rec.Name, rec.Name)
	}
	fmt.Printf("\n  devstack stack status %s   ·   devstack service restart <service> --stack %s\n", rec.Name, rec.Name)
	return nil
}

// stackPortLabel drops the "/http" port key, which repeats on every line and names
// nothing the reader picked. Any other key stays: it is the only thing telling
// two of a service's ports apart.
func stackPortLabel(key string) string {
	return strings.TrimSuffix(key, "/http")
}

func runStackStatusCmd(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	rec, err := stack.Resolve(base.Name, args[0])
	if err != nil {
		return err
	}
	return runStackStatus(base, rec)
}

func runStackDown(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	rec, err := stack.Resolve(base.Name, args[0])
	if err != nil {
		return err
	}

	fireTeardownHooks(base, rec.Name, config.EventStackDown, nil)

	if err := stack.MarkDown(base.Name, rec.Name, stack.StoppedByUser); err != nil {
		return err
	}
	defer herdr.Notify()

	_, kept, err := regenerateHostTiltfileScope(hostdaemon.ScopeStack(base.Name, rec.Name))
	if err != nil {
		return fmt.Errorf("can not generate the host Tiltfile again: %w", err)
	}
	fmt.Printf("✓ Regenerated the host Tiltfile. The host daemon will drop the resources of stack %q.\n", rec.Name)
	if note := hostdaemon.KeptNote(kept); note != "" {
		fmt.Println(note)
	}

	fmt.Printf("✓ Stack %q is now down. devstack keeps its worktrees and its record. Remove them with: devstack stack rm %s\n", rec.Name, rec.Name)
	return nil
}

func stackFlagRecord(wsName, stackName string) (*stack.Record, error) {
	if stackName == "" {
		return nil, nil
	}
	return stack.Resolve(wsName, stackName)
}

func resolveStackTarget(base *workspace.Workspace, stackName string) (port int, namespace string, root string, label string, err error) {
	if stackName == "" {
		return workspace.HostTiltPort, "", base.Path, "", nil
	}
	rec, err := stack.Resolve(base.Name, stackName)
	if err != nil {
		if recs, lerr := stack.LoadStore(base.Name); lerr == nil && len(recs) > 0 {
			avail := make([]string, 0, len(recs))
			for _, r := range recs {
				avail = append(avail, r.Name)
			}
			return 0, "", "", "", fmt.Errorf("stack %q not found in workspace %q. Available stacks: %s", stackName, base.Name, strings.Join(avail, ", "))
		}
		return 0, "", "", "", err
	}
	baseUp := isTiltReachable(fmt.Sprintf("http://localhost:%d/api/view", workspace.HostTiltPort))
	if !baseUp || !rec.Active {
		return 0, "", "", "", fmt.Errorf("stack %q is not up — run: devstack stack up %s", stackName, rec.Name)
	}
	return workspace.HostTiltPort, rec.Name, rec.Root, rec.FullName(), nil
}

func resourceName(wsName, svc, namespace string) string {
	if namespace == "" {
		return wsName + ":" + svc
	}
	return wsName + ":" + svc + ":" + namespace
}

func sortedServiceNames(rw *config.ResolvedWorkspace) []string {
	names := make([]string, 0, len(rw.Services))
	for n := range rw.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
