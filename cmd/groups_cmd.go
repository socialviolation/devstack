package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
)

var groupsCmd = &cobra.Command{
	Use:   "groups",
	Short: "Manage service groups in .devstack.json",
}

var groupsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all declared groups and their members",
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

	for _, sub := range []*cobra.Command{groupsListCmd, groupsAddCmd, groupsRemoveCmd} {
		sub.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	}
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
		fmt.Println("No groups declared. Use: devstack groups add <group> <service> [service...]")
		return nil
	}

	names := make([]string, 0, len(cfg.Groups))
	for name := range cfg.Groups {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		fmt.Printf("%-20s  %s\n", name, strings.Join(cfg.Groups[name], ", "))
	}
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
		fmt.Printf("✓ Group %q is now empty (remove it by editing .devstack.json if no longer needed)\n", group)
	} else {
		fmt.Printf("✓ Group %q: %s\n", group, strings.Join(remaining, ", "))
	}
	return nil
}
