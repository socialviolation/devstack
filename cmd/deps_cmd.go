package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hostdaemon"
)

var depsCmd = &cobra.Command{
	Use:     "dependencies",
	Aliases: []string{"deps"},
	Short:   "Edit the old dependencies: map of the workspace manifest",
	Long: `These commands edit the old "dependencies:" map in
<workspace>/devstack.workspace.yaml. devstack loads that map as "calls" edges.
A calls edge does not pull a service into a stack, and the host daemon does
not wait for it.

To declare an edge, use "needs" or "calls" in devstack.service.yaml. A needs
edge pulls the service into a stack, and the host daemon starts it first.

'devstack service start' and 'devstack group start' still read this map. They
start the services it lists first, in order.

Example:
  devstack dependencies add api postgres
  devstack service start api --stack feat   ← starts postgres, then api

SUBCOMMANDS
  devstack dependencies add <svc> <dep>    declare that svc needs dep to run first
  devstack dependencies remove <svc> <dep> remove a declared dependency
  devstack dependencies list <svc>         show the full resolved startup sequence for a service`,
}

var depsAddCmd = &cobra.Command{
	Use:   "add <service> <dep>",
	Short: "Add <dep> to the dependencies of <service> in the old map",
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
	Use:     "list <service>",
	Aliases: []string{"order"},
	Short:   "Show the full resolved startup sequence for a service",
	Long: `devstack resolves the complete dependency graph for a service and prints the
startup order from the old dependencies: map. That is the sequence that
'devstack service start <service>' uses.

Use this command to make sure that the dependencies are correct.`,
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

	if !config.HasWorkspaceManifest(ws.Path) {
		return fmt.Errorf("workspace %q has no %s. devstack can edit dependencies only in a workspace that has a manifest", ws.Name, config.WorkspaceManifestFileName)
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	// Check for duplicate
	for _, existing := range cfg.Deps[service] {
		if existing == dep {
			fmt.Printf("%s is already a declared dependency of %s\n", dep, service)
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
		return fmt.Errorf("dependency cycle detected: %q as a dependency of %q makes a cycle", dep, service)
	}

	if err := config.SetServiceDependencies(ws.Path, service, cfg.Deps[service]); err != nil {
		return err
	}

	fmt.Printf("✓ %s now depends on %s\n", service, dep)
	if _, _, err := regenerateHostTiltfileScope(hostdaemon.ScopeWorkspace(ws.Name)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: can not regenerate the host configuration: %v\n", err)
	} else {
		fmt.Println("Regenerated the host configuration.")
	}
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

	if !config.HasWorkspaceManifest(ws.Path) {
		return fmt.Errorf("workspace %q has no %s. devstack can edit dependencies only in a workspace that has a manifest", ws.Name, config.WorkspaceManifestFileName)
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
		fmt.Printf("%s is not a dependency of %s\n", dep, service)
		return nil
	}

	cfg.Deps[service] = newDeps

	if err := config.SetServiceDependencies(ws.Path, service, newDeps); err != nil {
		return err
	}

	fmt.Printf("✓ Removed %s from %s dependencies\n", dep, service)
	if _, _, err := regenerateHostTiltfileScope(hostdaemon.ScopeWorkspace(ws.Name)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: can not regenerate the host configuration: %v\n", err)
	} else {
		fmt.Println("Regenerated the host configuration.")
	}
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
