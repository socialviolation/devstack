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
	Long: `Show a live table of every service in the current workspace — the group or
feature stack it belongs to, its running state, exposed ports, and declared
dependencies.

If run from outside any registered workspace, shows a summary table of all
workspaces and their daemon status instead.

Groups and feature stacks with nothing running collapse to a single line.
Pass --all to table every group and stack, and to show each service's source path.

Service states:
  running   — process is up and healthy
  starting  — process is coming up
  building  — the daemon is building/updating the service
  stopped   — service is registered but not currently running
  erroring  — the service or its build failed (check logs)
  disabled  — service has been explicitly stopped
  unknown   — daemon is not reachable (run: devstack workspace up)`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().String("stack", "", "Show a feature stack's service instances (<ws>:<svc>:<stack>) instead of base")
	statusCmd.Flags().Bool("all", false, "Show the full table for every group and stack, including ones with nothing running")
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
	expand, _ := cmd.Flags().GetBool("all")
	return runWorkspaceStatus(ws, expand)
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

func runWorkspaceStatus(ws *workspace.Workspace, expand bool) error {
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
		resourceMap = hostResourceMap(view.UiResources, ws.Name, "")
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
	otelUp := isOtelRunning(ws)
	pluginConfigured := ws.OtelPlugin != "" || len(ws.OtelPluginConfig) > 0
	otelText, decided := otelSegment(otelUp, config.ObservabilityEnabled(ws.Path), pluginConfigured,
		ws.OtelPlugin, ws.UIPort(), ws.HTTPPort(), ws.GRPCPort())
	switch {
	case decided && otelUp:
		infraParts = append(infraParts, otelText)
	case decided:
		infraParts = append(infraParts, color.New(color.FgYellow).Sprint(otelText))
	default:
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

	sections := make([]serviceSection, 0, len(groupNames)+1)
	for i, groupName := range groupNames {
		members := append([]string(nil), cfg.Groups[groupName]...)
		if len(members) == 0 {
			continue
		}
		sort.Strings(members)
		groupRunning := countRunning(members, resourceMap)
		sections = append(sections, serviceSection{
			label:     groupName,
			members:   members,
			deps:      cfg.Deps,
			resources: resourceMap,
			envs:      svcEnvNames,
			dirs:      serviceDirs,
			color:     groupPalette[i%len(groupPalette)],
			running:   groupRunning,
			tag:       fmt.Sprintf("[%d/%d]", groupRunning, len(members)),
		})
	}

	ungrouped := make([]string, 0)
	for svc := range allServices {
		if !inGroup[svc] {
			ungrouped = append(ungrouped, svc)
		}
	}
	sort.Strings(ungrouped)
	if len(ungrouped) > 0 {
		ungroupedRunning := countRunning(ungrouped, resourceMap)
		sections = append(sections, serviceSection{
			label:     ungroupedLabel,
			members:   ungrouped,
			deps:      cfg.Deps,
			resources: resourceMap,
			envs:      svcEnvNames,
			dirs:      serviceDirs,
			color:     color.New(color.Faint, color.Bold),
			running:   ungroupedRunning,
			tag:       fmt.Sprintf("[%d/%d]", ungroupedRunning, len(ungrouped)),
		})
	}
	sections = append(sections, stackSections(ws, view, cfg.Deps, len(groupNames))...)

	table, condensed := partitionSections(sections, expand)
	if rows := assembleRows(table); len(rows) > 0 {
		renderStatusTable(rows, svcGroupColor, expand)
	}
	if len(condensed) > 0 {
		fmt.Println()
		for _, s := range condensed {
			printCondensedSection(s.color, s.label, s.tag, s.members)
		}
	}
	fmt.Println()

	color.New(color.Faint).Printf("  within a group, top-to-bottom = startup order   ·   blank ENV = no env\n")
	color.New(color.Faint).Printf("  devstack start <service>   ·   devstack start --group=<group>\n")
	color.New(color.Faint).Printf("  devstack stack up <name>   ·   devstack stack config <svc> --stack <name>\n")
	color.New(color.Faint).Printf("  idle groups are condensed   ·   devstack status --all shows every service and its source path\n")

	return nil
}

// stackSections turns the workspace's in-flight feature stacks into table
// sections, reading each stack's <ws>:<svc>:<stack> resources out of the host
// view already fetched for the base workspace. colorOffset continues the group
// palette so a stack's rows stay distinguishable from the base groups'.
func stackSections(ws *workspace.Workspace, view *tilt.TiltView, baseDeps map[string][]string, colorOffset int) []serviceSection {
	recs, err := stack.LoadStore(ws.Name)
	if err != nil || len(recs) == 0 {
		return nil
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })

	out := make([]serviceSection, 0, len(recs))
	for _, rec := range recs {
		resourceMap := map[string]tilt.UIResource{}
		if view != nil {
			resourceMap = hostResourceMap(view.UiResources, ws.Name, rec.Name)
		}

		services := map[string]bool{}
		for _, svc := range rec.Overlay {
			services[svc] = true
		}
		for svc := range resourceMap {
			services[svc] = true
		}
		if len(services) == 0 {
			continue
		}

		deps := baseDeps
		rw, err := stack.ResolveWorktree(&rec)
		if err == nil && rw != nil {
			if stackCfg := rw.ToLegacyConfig(); stackCfg != nil && len(stackCfg.Deps) > 0 {
				deps = stackCfg.Deps
			}
		} else {
			rw, _ = config.ResolveWorkspace(ws.Path)
		}
		svcEnvNames := resolveActiveEnvs(rw, services, rec.Env)

		names := make([]string, 0, len(services))
		for svc := range services {
			names = append(names, svc)
		}
		sort.Strings(names)

		stackRunning := countRunning(names, resourceMap)
		tag := fmt.Sprintf("[%d/%d]", stackRunning, len(names))
		if !rec.Active {
			tag += " inactive"
		}

		// A stack runs its own worktree of each service, so its rows must report
		// that checkout rather than the base repo's.
		dirs := map[string]string{}
		if rw != nil {
			for _, name := range names {
				if svc, ok := rw.Services[name]; ok {
					dirs[name] = svc.RepoPath
				}
			}
		}

		out = append(out, serviceSection{
			label:     "stack: " + rec.Name,
			members:   names,
			deps:      deps,
			resources: resourceMap,
			envs:      svcEnvNames,
			dirs:      dirs,
			color:     groupPalette[(colorOffset+len(out))%len(groupPalette)],
			running:   stackRunning,
			tag:       tag,
			isStack:   true,
		})
	}
	return out
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
	case "erroring":
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
