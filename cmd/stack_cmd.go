package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
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

func init() {
	rootCmd.AddCommand(stackCmd)
	stackCmd.AddCommand(stackCreateCmd)
	stackCmd.AddCommand(stackRemoveCmd)
	stackCmd.AddCommand(stackListCmd)
	stackCmd.AddCommand(stackConfigCmd)

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

	fmt.Printf("\nStack %q ready. Start it: (cd %s && devstack up)\n", res.StackName, res.StackRoot)
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
