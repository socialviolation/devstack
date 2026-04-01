package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/tilt"
	"devstack/internal/workspace"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show live service status (or all workspaces if run outside a workspace)",
	Long: `Show a live grouped tree view of every service in the current workspace —
their running state, exposed ports, and declared dependencies.

If run from outside any registered workspace, shows a summary table of all
workspaces and their daemon status instead.

Service states:
  running   — process is up and healthy
  starting  — process is starting or building
  error     — process exited with an error (check logs)
  idle      — service is registered but not currently enabled
  disabled  — service has been explicitly stopped
  unknown   — daemon is not reachable (run: devstack workspace up)`,
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

// groupPalette cycles through distinct colors for group headers.
var groupPalette = []*color.Color{
	color.New(color.FgCyan, color.Bold),
	color.New(color.FgBlue, color.Bold),
	color.New(color.FgMagenta, color.Bold),
	color.New(color.FgYellow, color.Bold),
	color.New(color.FgGreen, color.Bold),
}

func runStatus(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace") // inherited persistent flag
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return runStatusAll()
	}
	return runWorkspaceStatus(ws)
}

func runWorkspaceStatus(ws *workspace.Workspace) error {
	if actual := workspace.ResolvePort(ws.Name); actual != 0 && actual != ws.TiltPort {
		ws.TiltPort = actual
	}

	cfg, _ := config.Load(ws.Path)

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	view, tiltErr := tiltClient.GetView()

	resourceMap := make(map[string]tilt.UIResource)
	if tiltErr == nil {
		for _, r := range view.UiResources {
			resourceMap[r.Metadata.Name] = r
		}
	}

	// Collect all known service names
	allServices := make(map[string]bool)
	for name := range resourceMap {
		allServices[name] = true
	}
	for _, members := range cfg.Groups {
		for _, m := range members {
			allServices[m] = true
		}
	}
	for svc, deps := range cfg.Deps {
		allServices[svc] = true
		for _, d := range deps {
			allServices[d] = true
		}
	}

	// Count running
	running := 0
	for name := range allServices {
		if r, ok := resourceMap[name]; ok && serviceStatus(r) == "running" {
			running++
		}
	}

	// Header — line 1: workspace identity + service health
	healthColor := color.New(color.FgGreen)
	if tiltErr != nil {
		healthColor = color.New(color.FgRed)
	} else if running < len(allServices) {
		healthColor = color.New(color.FgYellow)
	}
	fmt.Printf("%s  ·  %s\n",
		color.New(color.Bold).Sprint(ws.Name),
		healthColor.Sprintf("%d of %d running", running, len(allServices)),
	)

	// Header — line 2: infrastructure ports (faint, secondary)
	var infraParts []string
	if tiltErr != nil {
		infraParts = append(infraParts, color.New(color.FgRed).Sprint("daemon stopped"))
	} else {
		infraParts = append(infraParts, fmt.Sprintf("daemon :%d", ws.TiltPort))
	}
	if ws.OtelMode == "byo" {
		if ws.OtelEndpoint != "" {
			infraParts = append(infraParts, fmt.Sprintf("otel byo  push:%s", ws.OtelEndpoint))
		} else {
			infraParts = append(infraParts, "otel byo")
		}
	} else if isOtelRunning(ws.Name) {
		infraParts = append(infraParts,
			fmt.Sprintf("otel ui:%d otlp:%d grpc:%d", ws.UIPort(), ws.HTTPPort(), ws.GRPCPort()),
		)
	}
	color.New(color.Faint).Printf("  %s\n\n", strings.Join(infraParts, "  ·  "))

	if tiltErr != nil {
		apiURL := fmt.Sprintf("http://localhost:%d/api/view", ws.TiltPort)
		if isTiltReachable(apiURL) {
			fmt.Println("  Dev daemon is starting — run 'devstack status' again in a moment.")
		} else {
			fmt.Println("  Run: devstack up")
		}
		return nil
	}

	// Sorted group names
	groupNames := make([]string, 0, len(cfg.Groups))
	for g := range cfg.Groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	// Build service → group color map for cross-group dep highlighting
	svcGroupColor := make(map[string]*color.Color)
	for i, groupName := range groupNames {
		gc := groupPalette[i%len(groupPalette)]
		for _, member := range cfg.Groups[groupName] {
			svcGroupColor[member] = gc
		}
	}

	// Track which services belong to at least one group
	inGroup := make(map[string]bool)
	for _, members := range cfg.Groups {
		for _, m := range members {
			inGroup[m] = true
		}
	}

	for i, groupName := range groupNames {
		members := cfg.Groups[groupName]
		if len(members) == 0 {
			continue
		}
		sort.Strings(members)

		gc := groupPalette[i%len(groupPalette)]

		groupRunning := 0
		for _, svc := range members {
			if r, ok := resourceMap[svc]; ok && serviceStatus(r) == "running" {
				groupRunning++
			}
		}
		gc.Printf("● %s", groupName)
		color.New(color.Faint).Printf("  [%d/%d]\n", groupRunning, len(members))

		memberSet := make(map[string]bool, len(members))
		for _, m := range members {
			memberSet[m] = true
		}
		roots := buildGroupTree(members, cfg.Deps)
		renderStatusNodes(roots, "  ", resourceMap, cfg.Deps, memberSet, svcGroupColor)
		fmt.Println()
	}

	// Ungrouped services
	ungrouped := make([]string, 0)
	for svc := range allServices {
		if !inGroup[svc] {
			ungrouped = append(ungrouped, svc)
		}
	}
	sort.Strings(ungrouped)

	if len(ungrouped) > 0 {
		color.New(color.Faint, color.Bold).Printf("● ungrouped\n")
		for j, svc := range ungrouped {
			isLast := j == len(ungrouped)-1
			branch := "  ├── "
			if isLast {
				branch = "  └── "
			}
			statusStr, statusClr := svcStatusColor(svc, resourceMap)
			portsStr := svcPorts(svc, resourceMap)
			fmt.Print(branch)
			fmt.Printf("%-22s  ", svc)
			statusClr.Printf("%-10s", statusStr)
			fmt.Printf("  %s\n", portsStr)
		}
		fmt.Println()
	}

	color.New(color.Faint).Printf("  devstack start <service>   ·   devstack start --group=<group>\n")

	return nil
}

func svcStatusColor(svc string, resourceMap map[string]tilt.UIResource) (string, *color.Color) {
	r, ok := resourceMap[svc]
	if !ok {
		return "unknown", color.New(color.Faint)
	}
	s := serviceStatus(r)
	switch s {
	case "running":
		return s, color.New(color.FgGreen)
	case "error":
		return s, color.New(color.FgRed, color.Bold)
	case "building", "starting":
		return s, color.New(color.FgYellow)
	default:
		return s, color.New(color.Faint)
	}
}

func svcPorts(svc string, resourceMap map[string]tilt.UIResource) string {
	r, ok := resourceMap[svc]
	if !ok {
		return color.New(color.Faint).Sprint("<event-driven>")
	}
	ports := extractPorts(r.Status.EndpointLinks)
	if ports == "-" || ports == "" {
		return color.New(color.Faint).Sprint("<event-driven>")
	}
	return ports
}

