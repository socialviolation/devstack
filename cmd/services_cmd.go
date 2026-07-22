package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
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
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().String("stack", "", "Show a feature stack's service instances (<ws>:<svc>:<stack>) instead of base")
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
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return runStatusAll()
	}
	if stackName, _ := cmd.Flags().GetString("stack"); stackName != "" {
		rec, err := stack.Resolve(ws.Name, stackName)
		if err != nil {
			return err
		}
		return runStackStatus(ws, rec)
	}
	return runWorkspaceStatus(ws)
}

// runStackStatus shows a feature stack's services as they run in the one host
// daemon: it reads the host view and filters to the stack's
// <base>:<service>:<stack> resources, printing them de-namespaced. A stack is up
// only when the host daemon is running and the stack is active — otherwise it
// prints the same "not up" guidance the other --stack commands give, without
// dialing a dead port.
func runStackStatus(base *workspace.Workspace, rec *stack.Record) error {
	port := workspace.HostTiltPort
	if !isTiltReachable(fmt.Sprintf("http://localhost:%d/api/view", port)) || !rec.Active {
		fmt.Printf("stack %q is not up — run: devstack stack up %s\n", rec.Name, rec.Name)
		return nil
	}

	view, err := tilt.NewClient("localhost", port).GetView()
	if err != nil {
		return err
	}
	resourceMap := make(map[string]tilt.UIResource, len(view.UiResources))
	for _, r := range view.UiResources {
		resourceMap[r.Metadata.Name] = r
	}

	prefix := base.Name + ":"
	suffix := ":" + rec.Name
	names := make([]string, 0)
	for name := range resourceMap {
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	color.New(color.Bold).Printf("stack %q", rec.Name)
	color.New(color.Faint).Printf("  (in the host daemon :%d as %s<service>%s)\n\n", port, prefix, suffix)

	if len(names) == 0 {
		fmt.Printf("  No resources for stack %q in the host daemon yet.\n", rec.Name)
		return nil
	}

	baseRW, _ := config.ResolveWorkspace(base.Path)
	stackRW, _ := config.ResolveWorkspace(rec.Root)
	wsEnv := ""
	if baseRW != nil {
		wsEnv = baseRW.Manifest.Workspace.Env
	}
	envs := map[string]string{}
	for _, name := range names {
		svc := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		svcEnv := ""
		if stackRW != nil {
			if rs, ok := stackRW.Services[svc]; ok && rs.Manifest != nil {
				svcEnv = rs.Manifest.Service.Env
			}
		}
		if env := config.ActiveEnvName(wsEnv, svcEnv, rec.Env); env != "" {
			envs[svc] = env
		}
	}

	for _, name := range names {
		svc := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		statusStr, statusClr := svcStatusColor(name, resourceMap)
		fmt.Printf("  %-22s  ", svc)
		statusClr.Printf("%-10s", statusStr)
		fmt.Print("  ")
		printPorts(svcPortsRaw(name, resourceMap), 14)
		printEnv(svc, envs)
		fmt.Println()
	}
	return nil
}

func runWorkspaceStatus(ws *workspace.Workspace) error {
	ws.TiltPort = workspace.HostTiltPort

	cfg, _ := config.Load(ws.Path)
	serviceDirs := map[string]string{}
	if cfg != nil {
		serviceDirs = cfg.ServicePaths
	}

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	view, tiltErr := tiltClient.GetView()

	resourceMap := make(map[string]tilt.UIResource)
	if tiltErr == nil {
		prefix := ws.Name + ":"
		for _, r := range view.UiResources {
			bare, ok := strings.CutPrefix(r.Metadata.Name, prefix)
			if !ok || strings.Contains(bare, ":") {
				continue
			}
			resourceMap[bare] = r
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

	rw, _ := config.ResolveWorkspace(ws.Path)
	svcEnvNames := resolveActiveEnvs(rw, allServices, "")

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
		infraParts = append(infraParts, fmt.Sprintf("host daemon :%d", ws.TiltPort))
	}
	if isOtelRunning(ws) {
		infraParts = append(infraParts,
			fmt.Sprintf("otel ui:%d otlp:%d grpc:%d", ws.UIPort(), ws.HTTPPort(), ws.GRPCPort()),
		)
	} else if config.ObservabilityEnabled(ws.Path) {
		if started, err := ensureCollector(ws); started {
			infraParts = append(infraParts, color.New(color.FgGreen).Sprint("otel: collector was down — started it"))
		} else if err != nil {
			infraParts = append(infraParts, color.New(color.FgRed).Sprintf("otel DOWN (auto-start failed: %v) — run: devstack otel start", err))
		} else {
			infraParts = append(infraParts, color.New(color.FgRed).Sprint("otel configured but collector DOWN — run: devstack otel start"))
		}
	}
	if composeSpec, err := infra.ResolveComposeSpec(ws.Path); err == nil && composeSpec != nil {
		if running, err := infra.RunningServices(composeSpec); err == nil && len(running) > 0 {
			infraParts = append(infraParts, fmt.Sprintf("infra %s", strings.Join(running, ",")))
		}
	}
	color.New(color.Faint).Printf("  %s\n\n", strings.Join(infraParts, "  ·  "))

	if tiltErr != nil {
		apiURL := fmt.Sprintf("http://localhost:%d/api/view", ws.TiltPort)
		if isTiltReachable(apiURL) {
			fmt.Println("  Dev daemon is starting — run 'devstack status' again in a moment.")
		} else {
			fmt.Println("  Run: devstack workspace up")
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
		groupEnv := commonEnv(members, svcEnvNames)

		gc.Printf("● %s", groupName)
		color.New(color.Faint).Printf("  [%d/%d]", groupRunning, len(members))
		if groupEnv != "" {
			color.New(color.Faint).Printf("   env %s", groupEnv)
		}
		fmt.Println()
		fmt.Println()

		memberSet := make(map[string]bool, len(members))
		for _, m := range members {
			memberSet[m] = true
		}
		order := orderGroupServices(members, cfg.Deps)
		renderServiceRows(order, resourceMap, cfg.Deps, memberSet, svcGroupColor, serviceDirs, svcEnvNames, groupEnv)
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
		color.New(color.Faint, color.Bold).Printf("● ungrouped\n\n")
		memberSet := make(map[string]bool, len(ungrouped))
		for _, m := range ungrouped {
			memberSet[m] = true
		}
		renderServiceRows(orderGroupServices(ungrouped, cfg.Deps), resourceMap, cfg.Deps, memberSet, svcGroupColor, serviceDirs, svcEnvNames, "")
		fmt.Println()
	}

	printStackSection(ws.Name)

	color.New(color.Faint).Printf("  top-to-bottom = startup order   ·   blank ENV = group env\n")
	color.New(color.Faint).Printf("  devstack start <service>   ·   devstack start --group=<group>\n")

	return nil
}

// printStackSection lists the workspace's in-flight feature stacks under the base
// service tree, so a status check surfaces the other running versions. It prints
// nothing for a workspace with no stacks (or when the target is itself a stack,
// whose name has no store of its own).
func printStackSection(wsName string) {
	stacks, err := stack.List(wsName)
	if err != nil || len(stacks) == 0 {
		return
	}
	fmt.Println()
	color.New(color.Bold).Printf("Feature stacks of %s (%d in flight):\n", wsName, len(stacks))
	for _, s := range stacks {
		statusClr := color.New(color.Faint)
		if s.Status == "active" {
			statusClr = color.New(color.FgGreen)
		}
		fmt.Printf("  %-22s ", s.Name)
		statusClr.Printf("%-9s", s.Status)
		fmt.Printf("  base :%d", s.BasePort)
		links := make([]string, 0, len(s.Ports))
		for _, k := range sortedKeys(s.Ports) {
			links = append(links, fmt.Sprintf("%s→:%d", k, s.Ports[k]))
		}
		if len(links) > 0 {
			color.New(color.Faint).Printf("   %s", strings.Join(links, " "))
		}
		fmt.Println()
	}
	color.New(color.Faint).Printf("  devstack stack up <name>   ·   devstack stack config <svc> --stack <name>\n")
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

// svcPortsRaw returns the plain (uncolored) port string for a service.
func svcPortsRaw(svc string, resourceMap map[string]tilt.UIResource) string {
	r, ok := resourceMap[svc]
	if !ok {
		return "<event-driven>"
	}
	ports := extractPorts(r.Status.EndpointLinks)
	if ports == "-" || ports == "" {
		return "<event-driven>"
	}
	return ports
}

// resolveActiveEnvs maps each service to its active env name (stack beats service
// beats workspace), omitting services whose active env is empty.
func resolveActiveEnvs(rw *config.ResolvedWorkspace, services map[string]bool, stackEnv string) map[string]string {
	out := map[string]string{}
	if rw == nil {
		return out
	}
	wsEnv := rw.Manifest.Workspace.Env
	for name := range services {
		svcEnv := ""
		if rs, ok := rw.Services[name]; ok && rs.Manifest != nil {
			svcEnv = rs.Manifest.Service.Env
		}
		if env := config.ActiveEnvName(wsEnv, svcEnv, stackEnv); env != "" {
			out[name] = env
		}
	}
	return out
}

// printEnv prints a faint env tag for a service when it has an active env.
func printEnv(name string, envs map[string]string) {
	if env := envs[name]; env != "" {
		color.New(color.Faint).Printf("  env:%s", env)
	}
}

// printPorts prints the port string with consistent visible-width padding.
func printPorts(raw string, width int) {
	padded := fmt.Sprintf("%-*s", width, raw)
	if raw == "<event-driven>" {
		color.New(color.Faint).Print(padded)
	} else {
		fmt.Print(padded)
	}
}
