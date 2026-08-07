package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

var stackCmd = &cobra.Command{
	Use:   "stack",
	Short: "Create and manage feature stacks that overlay a base workspace",
	Long: `A feature stack runs some of a base workspace's services from their own git
worktrees. For every other service the stack uses base's copy.

The services that you change get a worktree and an allocated port. The services
that call them get one too. So does everything those services need: what they
start after, and what they call. A stack stands up what it needs. All the other
services resolve to base's copies.

"base" is the workspace that runs with no stack. Base does not run from your
checkouts. Base runs from a replica that devstack keeps: one git worktree for
each service, at the default branch tip. Your checkout is the template, and
devstack builds the replica from it.

Base is not a stack, and no stack can carry the name "base". A command that
starts, stops or writes takes --stack base to act on base. With no --stack, it
acts on the stack or the replica that your directory is in, and on base anywhere
else.

The telemetry queries are the exception. 'devstack otel traces' with no --stack
searches base alone. Pass --stack <name> for one stack, or --stack all for base
and every stack together.`,
	RunE: runStackList,
}

var stackCreateCmd = &cobra.Command{
	Use:   "create <name> --repos a,b",
	Short: "Create a feature stack overlaying the base workspace",
	Long: `Create a feature stack. devstack makes a git worktree and allocates a port for
each service that you name. It does the same for the services that call them,
and for everything those services need to run. Every other service resolves to
base's copy.

A service needs the services it starts after, and the services it calls. The
workspace manifest declares both. If a service you name has a need that the
manifest does not declare, the stack takes that one from base.

A stack starts from what the team shipped. devstack cuts each worktree from that
repo's default branch. Where the repo has a remote, devstack uses origin's copy
of that branch. devstack never cuts from what your checkout has checked out, so
half-finished work in a checkout does not come into the stack. --from names a
different ref: a release branch, a tag, or a commit.

If the branch already exists, devstack attaches to it with the history that it
already has. --from does not apply to a branch that already exists.

If a checkout holds uncommitted work, the command says so and carries on. It
does the same if the checkout holds commits that the stack's ref does not have.
That is a warning that this stack does not contain that work. It is not an
error.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackCreate,
}

var stackAddCmd = &cobra.Command{
	Use:   "add <name> <service|group> [service|group...]",
	Short: "Add services to a stack that already exists",
	Long: `Put another service into a stack, and disturb nothing that is already in it.
The worktrees that the stack has keep their branches and their work. The copies
that it runs keep their ports.

Each named service gets a worktree on the stack's branch. The services that call
it come in too, in the same way that 'stack create' brings them in. A name that
is already in the stack is reported, and that is not an error. If you name
nothing new at all, that is an error.

The stack stays exactly as up, or as down, as it was. If the stack is up, the
added copies become resources in the host daemon, and devstack does not start
them. Start each one with 'devstack service start <service> --stack <name>'.`,
	Args:         cobra.MinimumNArgs(2),
	SilenceUsage: true,
	RunE:         runStackAdd,
}

var stackRemoveCmd = &cobra.Command{
	Use:     "rm <name>",
	Aliases: []string{"remove"},
	Short:   "Stop a stack, remove its worktrees, release its ports, and delete its record",
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
	Short:        "List registered feature stacks",
	SilenceUsage: true,
	RunE:         runStackList,
}

var stackConfigCmd = &cobra.Command{
	Use:   "config <service>",
	Short: "Show the configuration a service would run with in one copy (read-only)",
	Long: `Show the configuration that devstack gives a service in one copy: the effective
service configuration, and the environment ladder that the process receives.

This command reads files. It does not ask the daemon what runs. If that copy is
down, the command says so, and it prints the configuration that the copy would
run with.

  --stack <name>  the stack's worktree of the service
  --stack base    base's copy, read from the replica that base runs from
  no --stack      the stack that holds the current directory. This command reads
                  a stack worktree, so it refuses in a plain checkout. It does
                  not fall back to base. For base, pass --stack base.

For base, the replica is the truth, and not your checkout: base runs the replica
worktrees. If devstack has built no replica yet, run 'devstack workspace up'.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackConfig,
}

var stackUpCmd = &cobra.Command{
	Use:          "up <name>",
	Short:        "Start a feature stack's services on their own ports, in the one host daemon",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackUp,
}

var stackStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show a feature stack's services as they run in the host daemon",
	Long: `Show the copies of the services in one stack: the state, the ports and the env
of each one. devstack reads them from the one host daemon, and prints them
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

func init() {
	rootCmd.AddCommand(stackCmd)
	stackCmd.AddCommand(stackCreateCmd)
	stackCmd.AddCommand(stackAddCmd)
	stackCmd.AddCommand(stackRemoveCmd)
	stackCmd.AddCommand(stackListCmd)
	stackCmd.AddCommand(stackNoteCmd)
	stackCmd.AddCommand(stackConfigCmd)
	stackCmd.AddCommand(stackUpCmd)
	stackCmd.AddCommand(stackDownCmd)
	stackCmd.AddCommand(stackStatusCmd)

	stackCreateCmd.Flags().String("repos", "", "Service or group names that this stack changes, separated by commas. A group expands to its members")
	stackCreateCmd.Flags().String("branch", "", "Branch for the changed repos (default: the stack name). devstack attaches to it if it already exists")
	stackCreateCmd.Flags().String("from", "", "Ref that devstack cuts the worktrees from (default: each repo's default branch, origin's copy of it where there is one). Never what your checkout has checked out")
	stackCreateCmd.Flags().String("note", "", "What this stack is for: a ticket URL, an issue key, or a sentence. 'devstack stack list' shows it")
	stackAddCmd.Flags().String("from", "", "Ref that devstack cuts the new worktrees from (default: each repo's default branch, origin's copy of it where there is one)")
	stackNoteCmd.Flags().String("add", "", "Append a dated entry that says where the work got to, instead of replacing the note")
	stackRemoveCmd.Flags().Bool("force", false, "Remove worktrees even if they have uncommitted changes. This destroys that work. You can not recover it")
	stackConfigCmd.Flags().String("stack", "", "Which copy to read: a stack name, or \"base\" for base's copy. Default: the stack that holds the current directory. In a plain checkout this command refuses, so pass \"base\" there")
}

func runStackCreate(cmd *cobra.Command, args []string) error {
	reposFlag, _ := cmd.Flags().GetString("repos")
	changed := splitCSV(reposFlag)
	if len(changed) == 0 {
		return fmt.Errorf("--repos is required. Name the service or services that this stack changes")
	}

	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}

	branchFlag, _ := cmd.Flags().GetString("branch")
	fromFlag, _ := cmd.Flags().GetString("from")
	noteFlag, _ := cmd.Flags().GetString("note")
	res, err := stack.Create(stack.CreateInput{Base: base, Name: args[0], Repos: changed, Branch: branchFlag, From: fromFlag, Note: noteFlag})
	if err != nil {
		return err
	}

	fmt.Printf("Base workspace: %s (%s)\n", res.BaseName, res.BasePath)
	fmt.Printf("Overlay (the services you changed, the services that call them, and what those need):\n")
	for _, m := range res.Overlay {
		fmt.Printf("  %-16s %s\n", m.Service, m.Note("changed"))
	}
	fmt.Printf("Stack root (sibling of base): %s\n", res.StackRoot)
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
	fmt.Printf("  ✓ recorded stack %q (base %q, down)\n", res.StackName, res.BaseName)
	fmt.Printf("Allocated service ports (key scheme: service/portKey):\n")
	for _, k := range sortedKeys(res.Ports) {
		fmt.Printf("  %-24s http://localhost:%d\n", k, res.Ports[k])
	}

	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	overlay := make([]string, 0, len(res.Overlay))
	for _, m := range res.Overlay {
		overlay = append(overlay, m.Service)
	}
	if err := fireHooks(base, args[0], config.EventStackCreate, overlay); err != nil {
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
			fmt.Printf("  %-16s %s\n", m.Service, m.Note("added"))
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
	fmt.Printf("Overlay is now: %s\n", strings.Join(res.Overlay, ", "))
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
	if _, err := regenerateHostTiltfile(); err != nil {
		return fmt.Errorf("can not generate the host Tiltfile again: %w", err)
	}
	syncHostTiltfile(tilt.NewClient("localhost", workspace.HostTiltPort))
	fmt.Printf("  ✓ host Tiltfile now carries %s (not started)\n", strings.Join(added, ", "))

	if err := fireHooks(base, args[0], config.EventStackUp, added); err != nil {
		return fmt.Errorf("%w\ndevstack added %s to the running stack %q, but its %s hooks did not finish. It is not fully provisioned. Fix the hook. Then run the hooks again:\n  devstack hooks run %s --stack %s --services %s",
			err, strings.Join(added, ", "), res.StackName, config.EventStackUp, config.EventStackUp, args[0], strings.Join(added, ","))
	}

	fmt.Printf("\nStart the added service(s): %s\n", strings.Join(startCommands(added, args[0]), "  ·  "))
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

	// Before anything is taken away: worktrees, ports and the record are all
	// still readable, so a teardown hook can de-provision what create provisioned.
	fireTeardownHooks(base, args[0], config.EventStackDestroy, nil)

	if err := stack.SetActive(base.Name, args[0], false); err != nil {
		return err
	}
	if isTiltReachable(fmt.Sprintf("http://localhost:%d/api/view", workspace.HostTiltPort)) {
		if _, gerr := regenerateHostTiltfile(); gerr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to regenerate host Tiltfile: %v\n", gerr)
		}
	}

	res, err := stack.Remove(base, args[0], force)
	if res != nil {
		fmt.Printf("Removing stack %q (base %q)\n", res.Name, res.BaseName)
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
	color.New(color.Faint).Println("SERVICES is the overlay: the services this stack runs its own copy of. It uses base's copy of every other service.")
	color.New(color.Faint).Println("STATUS up means registered, not running. Each copy has its own state. Show it with: devstack status --stack <name>")
	color.New(color.Faint).Println("\"covers group g (3/4 — x serves from base)\" is a group this stack was cut to cover. The stack overlays 3 of the 4 members. x is base's copy, and everybody shares it. A group action reaches only the members the stack overlays.")
	color.New(color.Faint).Println("Set what a stack is for with: devstack stack note <name> \"...\". Record where the work got to with --add")
	return nil
}

// shortStackName trims the '<base>--' prefix, since every parameter takes the
// short half.
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

--add appends a dated entry instead of replacing the purpose. Write where the
work got to. Then you can pick the work up next week without a read of the diff.
devstack keeps the last %d entries, each one of %d characters at most. One entry
for each step pushes out the entry that is worth a read.

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

	if stackName == "base" {
		return runBaseConfig(service)
	}

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
		if err != nil {
			return fmt.Errorf("this directory is not inside a feature stack. Pass --stack <name>, or --stack base for base")
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
		tense = "the configuration it would run with"
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
		if rerr := tiltgen.ResolveLayerRefs(layers, svc.Name, opts.Book); rerr != nil {
			fmt.Printf("\nEnvironment (serve_env ladder): unavailable: %v\n", rerr)
			return nil
		}
	}
	printEnvLadder(layers)
	return nil
}

// runBaseConfig is `stack config --stack base`. Base is not a stack and has no
// record, so it resolves through the replica instead: base runs from the
// replica worktrees, and the checkout is only the template they were built
// from.
func runBaseConfig(service string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	rw, err := replica.Resolve(ws)
	if errors.Is(err, replica.ErrNotBuilt) {
		return fmt.Errorf("devstack has not built the replica of workspace %q, and base runs from it. There is no configuration to read yet. To build the replica, run: devstack workspace up", ws.Name)
	}
	if err != nil {
		return fmt.Errorf("base runs from the replica of workspace %q. The replica is stale or incomplete. To rebuild it, run: devstack workspace up. devstack could not read it: %w", ws.Name, err)
	}
	svc, ok := rw.Services[service]
	if !ok {
		return fmt.Errorf("service %q is not in workspace %q. Its services: %s", service, ws.Name, strings.Join(sortedServiceNames(rw), ", "))
	}

	entries, err := svcconfig.EffectiveConfig(svc, ws.Name)
	if err != nil {
		return err
	}

	tense := "the configuration it runs with"
	if !ws.Active {
		tense = "the configuration it would run with"
		fmt.Printf("Base is down. Nothing runs with this configuration now. To start base, run: devstack workspace up\n")
	}
	printConfigTable(service, "base", tense, entries)
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

// runStackUp marks a stack active and folds its services into the one host Tilt
// daemon: it marks the base workspace active too (a stack only renders inside its
// base's block), regenerates the host Tiltfile (now including the stack's
// <base>:<svc>:<stack> resources), and ensures the host daemon runs, so Tilt
// hot-reloads the new resources. There is no per-stack daemon.
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
		return fmt.Errorf("can not mark the base workspace active: %w", err)
	}
	if err := stack.SetActive(base.Name, rec.Name, true); err != nil {
		return err
	}

	if _, err := regenerateHostTiltfile(); err != nil {
		return fmt.Errorf("can not generate the host Tiltfile again: %w", err)
	}
	if err := ensureHostDaemon(); err != nil {
		return err
	}

	tiltClient := tilt.NewClient("localhost", workspace.HostTiltPort)
	syncHostTiltfile(tiltClient)

	started, err := stack.StartServices(tiltClient, base.Name, rec)
	if err != nil {
		return err
	}
	if len(started) == 0 {
		return fmt.Errorf("stack %q has no services in the host daemon. Create it again: devstack stack rm %s && devstack stack create %s --repos <svc>", rec.Name, rec.Name, rec.Name)
	}

	fmt.Printf("✓ Stack %q started: %s\n", rec.Name, strings.Join(started, ", "))
	for _, k := range sortedKeys(rec.Ports) {
		fmt.Printf("  %-24s http://localhost:%d\n", stackPortLabel(k), rec.Ports[k])
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

	if err := stack.SetActive(base.Name, rec.Name, false); err != nil {
		return err
	}

	if _, err := regenerateHostTiltfile(); err != nil {
		return fmt.Errorf("can not generate the host Tiltfile again: %w", err)
	}
	fmt.Printf("✓ Regenerated the host Tiltfile. The host daemon will drop the resources of stack %q.\n", rec.Name)

	fmt.Printf("✓ Stack %q is now down. devstack keeps its worktrees and its record. Remove them with: devstack stack rm %s\n", rec.Name, rec.Name)
	return nil
}

// stackFlagRecord maps a --stack flag to the stack record it names. An absent
// flag and the literal "base" both name base, which is not a stack and has no
// record, so both return a nil record and no error.
func stackFlagRecord(wsName, stackName string) (*stack.Record, error) {
	if stackName == "" || stackName == "base" {
		return nil, nil
	}
	return stack.Resolve(wsName, stackName)
}

// resolveStackTarget maps a --stack flag to the daemon and namespace a command
// acts on. Every workspace and stack runs in the one host daemon on HostTiltPort,
// so the returned port is always HostTiltPort and the namespace selects the stack's
// resources. An empty stackName targets the base itself: empty namespace, base
// root. A stack is operable only when the host daemon is up and the stack is active
// (its resources are in the host Tiltfile).
func resolveStackTarget(base *workspace.Workspace, stackName string) (port int, namespace string, root string, label string, err error) {
	if stackName == "" || stackName == "base" {
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

// resourceName is the host-daemon resource name for a service, matching tiltgen's
// hostName scheme: <workspace>:<service> for a base-workspace service, or
// <workspace>:<service>:<stack> for a feature stack folded into the host Tiltfile.
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
