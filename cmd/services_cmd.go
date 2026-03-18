package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/tilt"
)

var servicesCmd = &cobra.Command{
	Use:   "services",
	Short: "Show all known services with runtime status, group membership, and declared deps",
	Long: `Show a unified view of every service in the workspace.

Combines data from Tilt (live status and ports) and .devstack.json (groups and deps).
If Tilt is not reachable, services from config are still shown with unknown status.`,
	RunE: runServices,
}

func init() {
	rootCmd.AddCommand(servicesCmd)
	servicesCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
}

func runServices(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	// Load config for groups and deps
	cfg, err := config.Load(ws.Path)
	if err != nil {
		return err
	}

	// Try to get Tilt view; errors are non-fatal
	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	view, tiltErr := tiltClient.GetView()

	// Build a map of service name -> UIResource for quick lookup
	resourceMap := make(map[string]tilt.UIResource)
	if tiltErr == nil {
		for _, r := range view.UiResources {
			resourceMap[r.Metadata.Name] = r
		}
	}

	// Collect all service names from all sources
	seen := make(map[string]bool)

	// From Tilt view
	if tiltErr == nil {
		for _, r := range view.UiResources {
			seen[r.Metadata.Name] = true
		}
	}

	// From config groups (values)
	for _, members := range cfg.Groups {
		for _, svc := range members {
			seen[svc] = true
		}
	}

	// From config deps (keys and values)
	for svc, deps := range cfg.Deps {
		seen[svc] = true
		for _, dep := range deps {
			seen[dep] = true
		}
	}

	// Sort alphabetically
	services := make([]string, 0, len(seen))
	for svc := range seen {
		services = append(services, svc)
	}
	sort.Strings(services)

	if len(services) == 0 {
		fmt.Printf("No services found for workspace '%s'.\n", ws.Name)
		return nil
	}

	// Build reverse group membership map: service -> []groupName
	groupMembership := make(map[string][]string)
	groupNames := make([]string, 0, len(cfg.Groups))
	for gname := range cfg.Groups {
		groupNames = append(groupNames, gname)
	}
	sort.Strings(groupNames)
	for _, gname := range groupNames {
		for _, member := range cfg.Groups[gname] {
			groupMembership[member] = append(groupMembership[member], gname)
		}
	}

	// Print table
	const (
		wService = 24
		wStatus  = 10
		wPorts   = 14
		wGroups  = 24
	)

	fmt.Printf("%-*s %-*s %-*s %-*s %s\n",
		wService, "SERVICE",
		wStatus, "STATUS",
		wPorts, "PORT(S)",
		wGroups, "GROUPS",
		"DEPS",
	)
	fmt.Println(strings.Repeat("─", wService+1+wStatus+1+wPorts+1+wGroups+1+30))

	for _, svc := range services {
		var status, ports string

		if tiltErr != nil {
			status = "unknown"
			ports = "-"
		} else {
			if r, ok := resourceMap[svc]; ok {
				status = serviceStatus(r)
				ports = extractPorts(r.Status.EndpointLinks)
			} else {
				status = "unknown"
				ports = "-"
			}
		}

		groups := "-"
		if gs, ok := groupMembership[svc]; ok && len(gs) > 0 {
			groups = strings.Join(gs, ", ")
		}

		deps := "-"
		if ds, ok := cfg.Deps[svc]; ok && len(ds) > 0 {
			deps = strings.Join(ds, ", ")
		}

		fmt.Printf("%-*s %-*s %-*s %-*s %s\n",
			wService, svc,
			wStatus, status,
			wPorts, ports,
			wGroups, groups,
			deps,
		)
	}

	return nil
}
