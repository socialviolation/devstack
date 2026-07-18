package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/stack"
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

func init() {
	rootCmd.AddCommand(stackCmd)
	stackCmd.AddCommand(stackCreateCmd)
	stackCmd.AddCommand(stackRemoveCmd)
	stackCmd.AddCommand(stackListCmd)

	stackCreateCmd.Flags().String("repos", "", "Comma-separated service names that this stack changes")
	stackRemoveCmd.Flags().Bool("force", false, "Remove worktrees even if they have uncommitted changes")
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

	res, err := stack.Create(stack.CreateInput{Base: base, Name: args[0], Repos: changed})
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
	fmt.Printf("  ✓ registered %q (base %q, daemon port %d)\n", res.StackName, res.BaseName, res.DaemonPort)
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

	res, err := stack.Remove(args[0], force)
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
			fmt.Printf("  ✓ deregistered %q\n", res.Name)
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
	stacks, err := stack.List()
	if err != nil {
		return err
	}
	if len(stacks) == 0 {
		fmt.Println("No stacks registered. Create one with: devstack stack create <name> --repos <svc>")
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
