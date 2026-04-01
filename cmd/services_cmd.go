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

var tiltStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show services grouped by group with live status",
	RunE:  runTiltStatus,
}

func init() {
	tiltCmd.AddCommand(tiltStatusCmd)
	tiltStatusCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect)")
}

// groupPalette cycles through distinct colors for group headers.
var groupPalette = []*color.Color{
	color.New(color.FgCyan, color.Bold),
	color.New(color.FgBlue, color.Bold),
	color.New(color.FgMagenta, color.Bold),
	color.New(color.FgYellow, color.Bold),
	color.New(color.FgGreen, color.Bold),
}

func runTiltStatus(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

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

	// Header
	tiltState := fmt.Sprintf("Tilt :%d", ws.TiltPort)
	if tiltErr != nil {
		tiltState = color.New(color.FgRed).Sprint("Tilt not running")
	}
	otelState := ""
	if ws.OtelMode == "byo" {
		otelState = fmt.Sprintf("  ·  otel byo")
	} else if isOtelRunning(ws.Name) {
		uiPort := ws.OtelUIPort
		if uiPort == 0 {
			uiPort = 8080
		}
		otelState = fmt.Sprintf("  ·  otel :%d", uiPort)
	}
	fmt.Printf("%s  ·  %s%s  ·  %d of %d running\n\n",
		color.New(color.Bold).Sprint(ws.Name),
		tiltState,
		otelState,
		running,
		len(allServices),
	)

	if tiltErr != nil {
		apiURL := fmt.Sprintf("http://localhost:%d/api/view", ws.TiltPort)
		if isTiltReachable(apiURL) {
			fmt.Println("  Tilt is starting — run 'devstack tilt status' again in a moment.")
		} else {
			fmt.Println("  Run: devstack tilt up")
		}
		return nil
	}

	// Sorted group names
	groupNames := make([]string, 0, len(cfg.Groups))
	for g := range cfg.Groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	// Track which services are in at least one group
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

		// Group header with running count
		groupRunning := 0
		for _, svc := range members {
			if r, ok := resourceMap[svc]; ok && serviceStatus(r) == "running" {
				groupRunning++
			}
		}
		gc.Printf("● %s", groupName)
		color.New(color.Faint).Printf("  [%d/%d]\n", groupRunning, len(members))

		for j, svc := range members {
			isLast := j == len(members)-1
			branch := "  ├── "
			if isLast {
				branch = "  └── "
			}

			statusStr, statusClr := svcStatusColor(svc, resourceMap)
			portsStr := svcPorts(svc, resourceMap)
			deps := cfg.Deps[svc]

			fmt.Print(branch)
			fmt.Printf("%-22s  ", svc)
			statusClr.Printf("%-10s", statusStr)
			fmt.Printf("  %s", portsStr)
			if len(deps) > 0 {
				color.New(color.Faint).Printf("  ← %s", strings.Join(deps, ", "))
			}
			fmt.Println()
		}
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

	color.New(color.Faint).Printf("  devstack tilt start <service>   ·   devstack tilt start --group=<group>\n")

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
