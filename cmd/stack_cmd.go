package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

var stackCmd = &cobra.Command{
	Use:   "stack",
	Short: "Create and manage feature stacks that overlay a base workspace",
	Long: `A feature stack runs a subset of a base workspace's services from their own
git worktrees, reusing the base stack for everything else. Only the services you
change (and the services that call them) get a worktree and a dynamically
allocated port; the rest resolve to the base stack.`,
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
	Use:          "rm <name>",
	Aliases:      []string{"remove"},
	Short:        "Stop a stack, remove its worktrees, release its ports, and deregister it",
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
	Short:        "Start a feature stack's daemon (its services on their allocated ports)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackUp,
}

var stackDownCmd = &cobra.Command{
	Use:          "down <name>",
	Short:        "Stop a feature stack's daemon (leaves its worktrees and record)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackDown,
}

func init() {
	rootCmd.AddCommand(stackCmd)
	stackCmd.AddCommand(stackCreateCmd)
	stackCmd.AddCommand(stackRemoveCmd)
	stackCmd.AddCommand(stackListCmd)
	stackCmd.AddCommand(stackConfigCmd)
	stackCmd.AddCommand(stackUpCmd)
	stackCmd.AddCommand(stackDownCmd)

	stackCreateCmd.Flags().String("repos", "", "Comma-separated service names that this stack changes")
	stackCreateCmd.Flags().String("branch", "", "Branch for the changed repos (default: the stack name). Attaches if it already exists.")
	stackRemoveCmd.Flags().Bool("force", false, "Remove worktrees even if they have uncommitted changes")
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
	res, err := stack.Create(stack.CreateInput{Base: base, Name: args[0], Repos: changed, Branch: branchFlag})
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
	fmt.Printf("  ✓ recorded stack %q (base %q, daemon port %d)\n", res.StackName, res.BaseName, res.DaemonPort)
	fmt.Printf("Allocated service ports (key scheme: service/portKey):\n")
	for _, k := range sortedKeys(res.Ports) {
		fmt.Printf("  %-24s http://localhost:%d\n", k, res.Ports[k])
	}

	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
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

	res, err := stack.Remove(base, args[0], force)
	if res != nil {
		fmt.Printf("Removing stack %q (base %q)\n", res.Name, res.BaseName)
		if res.DaemonPID > 0 {
			fmt.Printf("  ✓ stopped daemon (pid %d)\n", res.DaemonPID)
		}
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

	fmt.Printf("%-24s %-14s %-6s %-9s %s\n", "STACK", "BASE", "PORT", "STATUS", "LINKS")
	fmt.Println(strings.Repeat("-", 90))
	for _, s := range stacks {
		links := make([]string, 0, len(s.Ports))
		for _, k := range sortedKeys(s.Ports) {
			links = append(links, fmt.Sprintf("%s=http://localhost:%d", k, s.Ports[k]))
		}
		linkStr := "-"
		if len(links) > 0 {
			linkStr = strings.Join(links, " ")
		}
		fmt.Printf("%-24s %-14s %-6d %-9s %s\n", s.Name, s.BaseName, s.Port, s.Status, linkStr)
	}
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

	rw, err := config.ResolveWorkspace(rec.Root)
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

	fmt.Printf("Effective config for %s in stack %s (read-only: what it WOULD run with)\n\n", service, rec.FullName())
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
	return nil
}

// resolveStackTarget maps a --stack flag to the path and daemon port that CLI
// service commands (status/restart/stop) should act on. Empty name returns the
// base workspace unchanged. A named stack returns its synthesised root and own
// daemon port, erroring clearly (never hanging) when the stack is unknown or its
// daemon isn't running.
func runStackUp(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	rec, err := stack.Resolve(base.Name, args[0])
	if err != nil {
		return err
	}

	key := rec.RuntimeKey()
	pidFile := workspace.PIDFile(key)
	logFile := workspace.LogFile(key)
	dataDir := workspace.DataDir(key)
	apiURL := fmt.Sprintf("http://localhost:%d/api/view", rec.DaemonPort)

	if isTiltReachable(apiURL) {
		fmt.Printf("Stack %q already running (port %d).\n", rec.Name, rec.DaemonPort)
		return nil
	}
	if data, rerr := os.ReadFile(pidFile); rerr == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil {
			if isProcessAlive(pid) {
				fmt.Printf("Stack %q already running (pid %d, port %d).\n", rec.Name, pid, rec.DaemonPort)
				return nil
			}
			os.Remove(pidFile)
		}
	}

	if !stack.DaemonReachable(base.TiltPort) {
		fmt.Fprintf(os.Stderr, "warning: base %q daemon is not running on :%d — the stack reuses base's services and DB tunnel; start base first.\n", base.Name, base.TiltPort)
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir %s: %w", dataDir, err)
	}
	if _, err := regenerateStackTiltfile(rec); err != nil {
		return fmt.Errorf("failed to generate stack Tiltfile: %w", err)
	}
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", logFile, err)
	}
	defer lf.Close()

	tiltCmd := exec.Command("tilt", "up", "--host", "0.0.0.0", "--port", strconv.Itoa(rec.DaemonPort))
	tiltCmd.Dir = rec.Root
	tiltCmd.Stdout = lf
	tiltCmd.Stderr = lf
	tiltCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := tiltCmd.Start(); err != nil {
		return fmt.Errorf("failed to start stack daemon: %w", err)
	}
	pid := tiltCmd.Process.Pid
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		tiltCmd.Process.Kill()
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	fmt.Printf("Starting stack %q daemon (base %q)", rec.Name, base.Name)
	deadline := time.Now().Add(45 * time.Second)
	reached := false
	for time.Now().Before(deadline) {
		if isTiltReachable(apiURL) {
			reached = true
			break
		}
		fmt.Print(".")
		time.Sleep(2 * time.Second)
	}
	fmt.Println()
	if reached {
		fmt.Printf("✓ Stack %q up (pid %d, daemon :%d)\n", rec.Name, pid, rec.DaemonPort)
		for _, k := range sortedKeys(rec.Ports) {
			fmt.Printf("  %-24s http://localhost:%d\n", k, rec.Ports[k])
		}
	} else {
		fmt.Printf("Stack daemon started (pid %d) but not reachable on :%d yet — check logs: %s\n", pid, rec.DaemonPort, logFile)
	}
	return nil
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

	pidFile := workspace.PIDFile(rec.RuntimeKey())
	data, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		fmt.Printf("Stack %q is not running.\n", rec.Name)
		return nil
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil {
		return fmt.Errorf("invalid PID in %s: %w", pidFile, perr)
	}

	fmt.Printf("Stopping stack %q (pid %d)...\n", rec.Name, pid)
	client := tilt.NewClient("localhost", rec.DaemonPort)
	if view, verr := client.GetView(); verr == nil {
		for _, r := range view.UiResources {
			client.RunCLI("disable", r.Metadata.Name) //nolint:errcheck
		}
	}
	if proc, ferr := os.FindProcess(pid); ferr == nil {
		_ = proc.Kill()
	}
	os.Remove(pidFile)
	fmt.Printf("✓ Stack %q stopped (worktrees and record kept; remove with: devstack stack rm %s).\n", rec.Name, rec.Name)
	return nil
}

func resolveStackTarget(base *workspace.Workspace, stackName string) (path string, port int, label string, err error) {
	if stackName == "" {
		return base.Path, base.TiltPort, "", nil
	}
	rec, err := stack.Resolve(base.Name, stackName)
	if err != nil {
		if recs, lerr := stack.LoadStore(base.Name); lerr == nil && len(recs) > 0 {
			avail := make([]string, 0, len(recs))
			for _, r := range recs {
				avail = append(avail, r.Name)
			}
			return "", 0, "", fmt.Errorf("stack %q not found in workspace %q. Available stacks: %s", stackName, base.Name, strings.Join(avail, ", "))
		}
		return "", 0, "", err
	}
	if !stack.DaemonReachable(rec.DaemonPort) {
		return "", 0, "", fmt.Errorf("stack %q daemon is not running on :%d — start it: devstack stack up %s", stackName, rec.DaemonPort, rec.Name)
	}
	return rec.Root, rec.DaemonPort, rec.FullName(), nil
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
