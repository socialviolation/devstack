package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"devstack/internal/config"
)

var groupsCmd = &cobra.Command{
	Use:   "groups",
	Short: "Show and manage service groups",
	Long: `Groups are named collections of services used to organise the workspace and
start related services together. For example, a 'backend' group might contain
your API, worker, and scheduler services.

Running 'devstack groups' (or 'devstack groups list') shows a rich tree of all
groups, their members, and the dependencies each member declares — colour-coded
by group so cross-group dependencies are immediately visible.

Groups are declared in <workspace>/.devstack.json and are used by:
  devstack start --group=<name>    start all services in a group (respects deps)
  devstack status                  groups are the top-level sections in the tree

SUBCOMMANDS
  devstack groups                  show group tree with deps
  devstack groups add <g> <svc>    add a service to a group
  devstack groups remove <g> <svc> remove a service from a group`,
	// Default action: show the rich group tree
	RunE: runGroupsList,
}

var groupsListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show all groups with members and dependencies",
	Args:  cobra.NoArgs,
	RunE:  runGroupsList,
}

var groupsAddCmd = &cobra.Command{
	Use:   "add <group> <service> [service...]",
	Short: "Add services to a group (creates the group if it doesn't exist)",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runGroupsAdd,
}

var groupsRemoveCmd = &cobra.Command{
	Use:   "remove <group> <service> [service...]",
	Short: "Remove services from a group",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runGroupsRemove,
}

func init() {
	rootCmd.AddCommand(groupsCmd)
	groupsCmd.AddCommand(groupsListCmd)
	groupsCmd.AddCommand(groupsAddCmd)
	groupsCmd.AddCommand(groupsRemoveCmd)
}

func runGroupsList(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	if len(cfg.Groups) == 0 {
		fmt.Println("No groups declared.")
		fmt.Println("Use: devstack groups add <group> <service> [service...]")
		return nil
	}

	groupNames := make([]string, 0, len(cfg.Groups))
	for name := range cfg.Groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	// Build service → group color map for dep highlighting
	svcGroupColor := make(map[string]*color.Color)
	for i, groupName := range groupNames {
		gc := groupPalette[i%len(groupPalette)]
		for _, member := range cfg.Groups[groupName] {
			svcGroupColor[member] = gc
		}
	}

	for i, groupName := range groupNames {
		members := cfg.Groups[groupName]
		if len(members) == 0 {
			continue
		}
		sorted := make([]string, len(members))
		copy(sorted, members)
		sort.Strings(sorted)

		gc := groupPalette[i%len(groupPalette)]
		gc.Printf("● %s", groupName)
		color.New(color.Faint).Printf("  [%d]\n", len(members))

		for j, svc := range sorted {
			isLast := j == len(sorted)-1
			branch := "  ├── "
			if isLast {
				branch = "  └── "
			}

			fmt.Print(branch)
			fmt.Printf("%-24s", svc)

			deps := cfg.Deps[svc]
			if len(deps) > 0 {
				color.New(color.Faint).Print("  ← ")
				for k, dep := range deps {
					if k > 0 {
						color.New(color.Faint).Print(", ")
					}
					if c, ok := svcGroupColor[dep]; ok {
						c.Print(dep)
					} else {
						color.New(color.Faint).Print(dep)
					}
				}
			}
			fmt.Println()
		}
		fmt.Println()
	}

	// Ungrouped services that have deps declared
	inGroup := make(map[string]bool)
	for _, members := range cfg.Groups {
		for _, m := range members {
			inGroup[m] = true
		}
	}
	var ungrouped []string
	for svc := range cfg.Deps {
		if !inGroup[svc] && len(cfg.Deps[svc]) > 0 {
			ungrouped = append(ungrouped, svc)
		}
	}
	if len(ungrouped) > 0 {
		sort.Strings(ungrouped)
		color.New(color.Faint, color.Bold).Printf("● ungrouped\n")
		for j, svc := range ungrouped {
			isLast := j == len(ungrouped)-1
			branch := "  ├── "
			if isLast {
				branch = "  └── "
			}
			fmt.Print(branch)
			fmt.Printf("%-24s", svc)
			deps := cfg.Deps[svc]
			if len(deps) > 0 {
				color.New(color.Faint).Print("  ← ")
				color.New(color.Faint).Print(strings.Join(deps, ", "))
			}
			fmt.Println()
		}
		fmt.Println()
	}

	color.New(color.Faint).Printf("  devstack status for live service state\n")
	return nil
}

func runGroupsAdd(cmd *cobra.Command, args []string) error {
	group := args[0]
	services := args[1:]

	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	if cfg.Groups == nil {
		cfg.Groups = make(map[string][]string)
	}

	existing := make(map[string]bool, len(cfg.Groups[group]))
	for _, s := range cfg.Groups[group] {
		existing[s] = true
	}

	added := 0
	for _, svc := range services {
		if !existing[svc] {
			cfg.Groups[group] = append(cfg.Groups[group], svc)
			existing[svc] = true
			added++
		}
	}

	if added == 0 {
		fmt.Printf("All services already in group %q\n", group)
		return nil
	}

	if err := config.Save(ws.Path, cfg); err != nil {
		return err
	}

	fmt.Printf("✓ Group %q: %s\n", group, strings.Join(cfg.Groups[group], ", "))
	return nil
}

func runGroupsRemove(cmd *cobra.Command, args []string) error {
	group := args[0]
	toRemove := make(map[string]bool)
	for _, s := range args[1:] {
		toRemove[s] = true
	}

	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	if _, ok := cfg.Groups[group]; !ok {
		return fmt.Errorf("group %q not found", group)
	}

	var remaining []string
	for _, svc := range cfg.Groups[group] {
		if !toRemove[svc] {
			remaining = append(remaining, svc)
		}
	}
	cfg.Groups[group] = remaining

	if err := config.Save(ws.Path, cfg); err != nil {
		return err
	}

	if len(remaining) == 0 {
		fmt.Printf("✓ Group %q is now empty\n", group)
	} else {
		fmt.Printf("✓ Group %q: %s\n", group, strings.Join(remaining, ", "))
	}
	return nil
}
