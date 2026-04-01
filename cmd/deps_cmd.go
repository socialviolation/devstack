package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"devstack/internal/config"
)

var depsCmd = &cobra.Command{
	Use:   "deps",
	Short: "Declare startup dependencies between services",
	Long: `Dependencies tell devstack which services must be running before a given service
can start. When you run 'devstack start api', devstack resolves the full dependency
graph and starts everything in the correct order automatically.

Dependencies are stored in <workspace>/.devstack.json and visualised inline in
the output of 'devstack groups' and 'devstack status'.

Example: if 'api' depends on 'postgres' and 'redis':
  devstack deps add api postgres
  devstack deps add api redis
  devstack start api          ← starts postgres, redis, then api

SUBCOMMANDS
  devstack deps add <svc> <dep>    declare that svc requires dep to be running first
  devstack deps remove <svc> <dep> remove a declared dependency
  devstack deps order <svc>        show the full resolved startup sequence for a service`,
}

var depsAddCmd = &cobra.Command{
	Use:   "add <service> <dep>",
	Short: "Declare that <service> depends on <dep> (dep starts first)",
	Args:  cobra.ExactArgs(2),
	RunE:  runDepsAdd,
}

var depsRemoveCmd = &cobra.Command{
	Use:   "remove <service> <dep>",
	Short: "Remove a declared dependency",
	Args:  cobra.ExactArgs(2),
	RunE:  runDepsRemove,
}

var depsOrderCmd = &cobra.Command{
	Use:   "order <service>",
	Short: "Show the full resolved startup sequence for a service",
	Long: `Resolves the complete dependency graph for a service and prints the startup
order — the sequence devstack will use when you run 'devstack start <service>'.
Useful for verifying that dependencies are declared correctly.`,
	Args: cobra.ExactArgs(1),
	RunE: runDepsOrder,
}

func init() {
	rootCmd.AddCommand(depsCmd)
	depsCmd.AddCommand(depsAddCmd)
	depsCmd.AddCommand(depsRemoveCmd)
	depsCmd.AddCommand(depsOrderCmd)
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

func runDepsOrder(cmd *cobra.Command, args []string) error {
	service := args[0]

	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	resolved, err := config.ResolveDeps(cfg, service)
	if err != nil {
		return err
	}

	fmt.Printf("Start order for %s:\n", service)
	for i, svc := range resolved {
		fmt.Printf("  %d. %s\n", i+1, svc)
	}
	return nil
}
