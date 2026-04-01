package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
)

var depsCmd = &cobra.Command{
	Use:   "deps",
	Short: "Manage service dependencies — shows all deps when run without a subcommand",
	RunE:  runDepsShow,
}

var depsAddCmd = &cobra.Command{
	Use:   "add <service> <dep>",
	Short: "Add a dependency: service depends on dep",
	Args:  cobra.ExactArgs(2),
	RunE:  runDepsAdd,
}

var depsRemoveCmd = &cobra.Command{
	Use:   "remove <service> <dep>",
	Short: "Remove a dependency from a service",
	Args:  cobra.ExactArgs(2),
	RunE:  runDepsRemove,
}

var depsShowCmd = &cobra.Command{
	Use:   "show [service]",
	Short: "Show dependencies (all services or resolved order for a specific service)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDepsShow,
}

func init() {
	rootCmd.AddCommand(depsCmd)
	depsCmd.AddCommand(depsAddCmd)
	depsCmd.AddCommand(depsRemoveCmd)
	depsCmd.AddCommand(depsShowCmd)

	depsCmd.PersistentFlags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
}

func runDepsAdd(cmd *cobra.Command, args []string) error {
	service := args[0]
	dep := args[1]

	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	// Check for duplicate
	for _, existing := range cfg.Deps[service] {
		if existing == dep {
			fmt.Printf("%s already declared as a dependency of %s\n", dep, service)
			return nil
		}
	}

	// Tentatively add the dep
	cfg.Deps[service] = append(cfg.Deps[service], dep)

	// Cycle detection: try to resolve deps for the service
	if _, err := config.ResolveDeps(cfg, service); err != nil {
		// Restore original state (remove the dep we just added)
		deps := cfg.Deps[service]
		cfg.Deps[service] = deps[:len(deps)-1]
		return fmt.Errorf("dependency cycle detected: adding %q as a dep of %q would create a cycle", dep, service)
	}

	if err := config.Save(ws.Path, cfg); err != nil {
		return err
	}

	fmt.Printf("✓ %s now depends on %s\n", service, dep)
	return nil
}

func runDepsRemove(cmd *cobra.Command, args []string) error {
	service := args[0]
	dep := args[1]

	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	existing := cfg.Deps[service]
	newDeps := make([]string, 0, len(existing))
	found := false
	for _, d := range existing {
		if d == dep {
			found = true
			continue
		}
		newDeps = append(newDeps, d)
	}

	if !found {
		fmt.Printf("%s not found in %s dependencies\n", dep, service)
		return nil
	}

	cfg.Deps[service] = newDeps

	if err := config.Save(ws.Path, cfg); err != nil {
		return err
	}

	fmt.Printf("✓ Removed %s from %s dependencies\n", dep, service)
	return nil
}

func runDepsShow(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	if len(args) == 1 {
		// Resolve deps for a specific service
		service := args[0]
		resolved, err := config.ResolveDeps(cfg, service)
		if err != nil {
			return err
		}
		fmt.Printf("Resolved start order for %s:\n", service)
		for i, svc := range resolved {
			fmt.Printf("  %d. %s\n", i+1, svc)
		}
		return nil
	}

	// No arg: print full dep graph for all services
	if len(cfg.Deps) == 0 {
		fmt.Println("No dependencies declared.")
		return nil
	}

	// Sort service names for deterministic output
	services := make([]string, 0, len(cfg.Deps))
	for svc := range cfg.Deps {
		services = append(services, svc)
	}
	sort.Strings(services)

	for _, svc := range services {
		deps := cfg.Deps[svc]
		if len(deps) == 0 {
			continue
		}
		fmt.Printf("%-30s →  %s\n", svc, strings.Join(deps, ", "))
	}

	return nil
}
