package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

var stackCmd = &cobra.Command{
	Use:   "stack",
	Short: "Create and manage feature stacks that overlay a base workspace",
	Long: `A feature stack runs a subset of a base workspace's services from their own
git worktrees, reusing base's copies for everything else. Only the services you
change (and the services that call them) get a worktree and a dynamically
allocated port; the rest resolve to base's copies.

"base" is the workspace running without any stack: your normal checkouts and the
copies started from them. It is not itself a stack, and no stack may be named
"base" — a command that starts, stops or writes, given no --stack, acts on base.

The telemetry queries are the exception: 'devstack otel traces' with no --stack
searches every copy, base and stacks together. Pass --stack base for base alone.`,
	RunE: runStackList,
}

var stackCreateCmd = &cobra.Command{
	Use:          "create <name> --repos a,b",
	Short:        "Create a feature stack overlaying the base workspace",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackCreate,
}

var stackRemoveCmd = &cobra.Command{
	Use:     "rm <name>",
	Aliases: []string{"remove"},
	Short:   "Stop a stack, remove its worktrees, release its ports, and deregister it",
	Long: `Tear down a feature stack: stop its services, delete its worktrees, release
its ports, and delete its record and its stack root.

The branch stays. Commits you pushed stay. Work that is only in a worktree does
not: deleting the worktree deletes it.

CAUTION: this command cannot be undone.

  --force  Deletes worktrees that have uncommitted changes, and destroys those
           changes. Without it the command refuses and names the dirty
           worktrees, which is your chance to commit them.

If this workspace declares stack.destroy hooks, they fire first, while the ports
and the record can still be read. A hook failure does not stop the teardown, so
it means the external cleanup probably did not happen — and you cannot retry it
afterwards, because the record its ${self...} references resolve against is gone.
The resolved URLs are printed at the point of failure. Keep them.`,
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
	Use:          "config <service>",
	Short:        "Show the effective config a service would run with in a stack (read-only)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackConfig,
}

var stackUpCmd = &cobra.Command{
	Use:          "up <name>",
	Short:        "Bring a feature stack's services up on their own ports in the one host daemon",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackUp,
}

var stackStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show a feature stack's services as they run in the host daemon",
	Long: `Show one stack's service instances: their state, ports and env, read from the
one host daemon and printed de-namespaced.

'devstack status' is the workspace-level view and takes --stack for the same
report.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackStatusCmd,
}

var stackDownCmd = &cobra.Command{
	Use:          "down <name>",
	Short:        "Stop a feature stack's services in the host daemon (leaves its worktrees and record)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackDown,
}

func init() {
	rootCmd.AddCommand(stackCmd)
	stackCmd.AddCommand(stackCreateCmd)
	stackCmd.AddCommand(stackRemoveCmd)
	stackCmd.AddCommand(stackListCmd)
	stackCmd.AddCommand(stackNoteCmd)
	stackCmd.AddCommand(stackConfigCmd)
	stackCmd.AddCommand(stackUpCmd)
	stackCmd.AddCommand(stackDownCmd)
	stackCmd.AddCommand(stackStatusCmd)

	stackCreateCmd.Flags().String("repos", "", "Comma-separated service names that this stack changes")
	stackCreateCmd.Flags().String("branch", "", "Branch for the changed repos (default: the stack name). Attaches if it already exists.")
	stackCreateCmd.Flags().String("note", "", "What this stack is for — a ticket URL, an issue key, a sentence. Shown by 'devstack stack list'.")
	stackRemoveCmd.Flags().Bool("force", false, "Remove worktrees even if they have uncommitted changes. Destroys that work; it cannot be recovered")
	stackConfigCmd.Flags().String("stack", "", "Stack name (default: the stack containing the current directory)")
}

func runStackCreate(cmd *cobra.Command, args []string) error {
	reposFlag, _ := cmd.Flags().GetString("repos")
	changed := splitCSV(reposFlag)
	if len(changed) == 0 {
		return fmt.Errorf("--repos is required: name the service(s) this stack changes")
	}

	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}

	branchFlag, _ := cmd.Flags().GetString("branch")
	noteFlag, _ := cmd.Flags().GetString("note")
	res, err := stack.Create(stack.CreateInput{Base: base, Name: args[0], Repos: changed, Branch: branchFlag, Note: noteFlag})
	if err != nil {
		return err
	}

	fmt.Printf("Base workspace: %s (%s)\n", res.BaseName, res.BasePath)
	fmt.Printf("Overlay set (changed ∪ transitive callers):\n")
	for _, m := range res.Overlay {
		note := "pulled in (calls a changed service)"
		if m.Reason == "changed" {
			note = "changed"
		}
		fmt.Printf("  %-16s %s\n", m.Service, note)
	}
	fmt.Printf("Stack root (sibling of base): %s\n", res.StackRoot)
	for _, wt := range res.Worktrees {
		branchNote := "detached at HEAD"
		if wt.Branch != "" {
			branchNote = "branch " + wt.Branch
		}
		fmt.Printf("  ✓ worktree %-16s %s (%s)\n", wt.Service, wt.Path, branchNote)
		if len(wt.Materialized) > 0 {
			fmt.Printf("    ↳ materialized %d local config file(s): %s\n", len(wt.Materialized), strings.Join(wt.Materialized, ", "))
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
		return fmt.Errorf("%w\nStack %q was created but its setup hooks did not finish. Fix the hook, then either re-run them:\n  devstack hooks run stack.create --stack %s\nor discard the stack:\n  devstack stack rm %s", err, res.StackName, args[0], args[0])
	}

	fmt.Printf("\nStack %q ready. Start it: devstack stack up %s\n", res.StackName, args[0])
	return nil
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
	if len(stacks) == 0 {
		fmt.Printf("No stacks in workspace %q. Create one with: devstack stack create <name> --repos <svc>\n", base.Name)
		return nil
	}

	fmt.Println("A stack that is up has its services registered in the one host daemon, namespaced <workspace>:<service>:<stack>.")
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
		var links []string
		for _, k := range sortedKeys(s.Ports) {
			links = append(links, fmt.Sprintf("%s=http://localhost:%d", k, s.Ports[k]))
		}
		if len(links) > 0 {
			color.New(color.Faint).Printf("  %s\n", strings.Join(links, "  "))
		}
	}
	fmt.Println()
	color.New(color.Faint).Println("SERVICES is the overlay: the services this stack runs its own copy of. Everything else it borrows from base.")
	color.New(color.Faint).Println("STATUS up means registered, not running. Each copy has its own state — see it with: devstack status --stack <name>")
	color.New(color.Faint).Println("Set what a stack is for with: devstack stack note <name> \"...\"")
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
	Long: `Record what a stack is for, in your words — a ticket URL, an issue key, a
sentence. devstack never derives this: a branch says what changed, a note says
why, and a week later the note is the part you cannot reconstruct.

With no text, prints the current note. Pass an empty string to clear it.

Examples:
  devstack stack note perf "NAV-412 daily value spike"
  devstack stack note perf https://linear.app/navexa/issue/NAV-412
  devstack stack note perf`,
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

	if len(args) == 1 {
		if rec.Note == "" {
			fmt.Printf("No note on stack %q. Set one with: devstack stack note %s \"...\"\n", rec.Name, rec.Name)
			return nil
		}
		fmt.Println(rec.Note)
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
		if err != nil {
			return fmt.Errorf("not inside a feature stack; pass --stack <name>")
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
		return fmt.Errorf("service %q not found in stack %q; services: %s", service, rec.FullName(), strings.Join(sortedServiceNames(rw), ", "))
	}

	entries, err := svcconfig.EffectiveConfig(svc, rec.RuntimeKey())
	if err != nil {
		return err
	}

	fmt.Printf("Effective config for %s in stack %s (read-only: what it WOULD run with)\n", service, rec.FullName())
	fmt.Printf("  %-42s %-12s %s\n", "KEY", "SOURCE", "VALUE")
	fmt.Println(strings.Repeat("-", 90))
	for _, e := range entries {
		marker := "  "
		if e.Overridden {
			marker = "* "
		}
		fmt.Printf("%s%-42s %-12s %s\n", marker, e.Key, e.Source, e.Value)
	}
	fmt.Printf("\n* = overridden by the stack (devstack-computed). Secret values shown as %s.\n", "••••")

	names := make([]string, 0, len(rw.Services))
	for n := range rw.Services {
		names = append(names, n)
	}
	opts, oerr := stack.GenerateOptions(rec, names)
	var managed map[string]string
	if oerr == nil {
		managed = opts.ManagedEnv[svc.Name]
	}
	layers, lerr := config.EnvLadder(svc.EnvDir(), rw.Manifest, svc.Manifest, rec.Env, managed)
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
	return nil
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
		return fmt.Errorf("failed to mark base workspace active: %w", err)
	}
	if err := stack.SetActive(base.Name, rec.Name, true); err != nil {
		return err
	}

	if _, err := regenerateHostTiltfile(); err != nil {
		return fmt.Errorf("failed to regenerate host Tiltfile: %w", err)
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
		return fmt.Errorf("stack %q has no services in the host daemon — recreate it: devstack stack rm %s && devstack stack create %s --repos <svc>", rec.Name, rec.Name, rec.Name)
	}

	fmt.Printf("✓ Stack %q started: %s\n", rec.Name, strings.Join(started, ", "))
	for _, k := range sortedKeys(rec.Ports) {
		fmt.Printf("  %-24s http://localhost:%d\n", stackPortLabel(k), rec.Ports[k])
	}

	if err := fireHooks(base, rec.Name, config.EventStackUp, started); err != nil {
		return fmt.Errorf("%w\nStack %q runs but its setup hooks did not finish. Fix the hook, then re-run them:\n  devstack hooks run stack.up --stack %s", err, rec.Name, rec.Name)
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
		return fmt.Errorf("failed to regenerate host Tiltfile: %w", err)
	}
	fmt.Printf("✓ Regenerated host Tiltfile — host daemon will drop stack %q's resources.\n", rec.Name)

	fmt.Printf("✓ Stack %q is now down (worktrees and record kept; remove with: devstack stack rm %s).\n", rec.Name, rec.Name)
	return nil
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
