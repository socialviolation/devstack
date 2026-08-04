package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show live service status (or all workspaces if run outside a workspace)",
	Long: `Show a live table of every service in the current workspace: the group or the
feature stack that it belongs to, its state, its ports, and its declared
dependencies.

If you run this command outside every registered workspace, devstack shows a
summary table of all the workspaces and their daemon status instead.

A group or a feature stack with nothing running collapses to one line. It stays
open when one of its services is erroring, starting or building. Pass --all to
list every group and stack, and to show the directory that each copy runs from.

That directory, and the BRANCH beside it, are the code that the process runs:
the replica worktree for base, and the stack's own worktree for a stack. It is
never your checkout. If you do not expect that branch there, the copy does not
contain the work that you look for.

A stack is up or down. Up means that its services are registered in the daemon.
It does not mean that they run. Each copy has its own state below.

Copy states:
  running   — the process is up and healthy
  starting  — the process starts
  building  — the daemon builds or updates the service
  stopped   — the service is registered, but it does not run now
  erroring  — the service or its build failed (read the logs)
  disabled  — somebody stopped the service
  down      — the copy is not registered at all, because its stack is down
              (run: devstack stack up <name>)
  unknown   — the daemon does not answer (run: devstack workspace up)`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().String("stack", "", "Show a feature stack's copies (<ws>:<svc>:<stack>) instead of base's")
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
// only when the host daemon runs and the stack is active — otherwise it
// prints the same "not up" guidance the other --stack commands give, without
// dialing a dead port.
func runStackStatus(base *workspace.Workspace, rec *stack.Record) error {
	port := workspace.HostTiltPort
	if !isTiltReachable(fmt.Sprintf("http://localhost:%d/api/view", port)) || !rec.Active {
		fmt.Printf("stack %q is not up. Run: devstack stack up %s\n", rec.Name, rec.Name)
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

// baseServiceDirs is where base's copies actually run: the replica worktrees,
// not the checkouts they were built from. The DIR and BRANCH columns are read to
// answer "is my work what is running", and pointing them at the template answers
// it wrongly — the checkout can sit on any branch, dirty, while base runs a
// detached worktree at the default branch tip. Falls back to the checkouts,
// which is then the truth, when no replica is built.
func baseServiceDirs(ws *workspace.Workspace, cfg *config.WorkspaceConfig) map[string]string {
	if rw, err := replica.Resolve(ws); err == nil {
		dirs := make(map[string]string, len(rw.Services))
		for name, svc := range rw.Services {
			dirs[name] = svc.RepoPath
		}
		return dirs
	}
	if cfg != nil {
		return cfg.ServicePaths
	}
	return map[string]string{}
}

func runWorkspaceStatus(ws *workspace.Workspace, expand bool) error {
	ws.TiltPort = workspace.HostTiltPort

	cfg, _ := config.Load(ws.Path)
	serviceDirs := baseServiceDirs(ws, cfg)

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
	header := fmt.Sprintf("%s  ·  %s",
		color.New(color.Bold).Sprint(ws.Name),
		healthColor.Sprintf("%d of %d running", running, len(allServices)),
	)
	if summary := stackRunningSummary(stackInstancesRunning(view, ws.Name)); summary != "" {
		header += "  ·  " + color.New(color.FgMagenta).Sprint(summary)
	}
	fmt.Println(header)

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
		ws.OtelPlugin, otel.QueryEndpointFor(ws), workspace.OTLPHTTPPort, workspace.OTLPGRPCPort)
	switch {
	case decided && otelUp:
		infraParts = append(infraParts, otelText)
	case decided:
		infraParts = append(infraParts, color.New(color.FgYellow).Sprint(otelText))
	default:
		if started, err := ensureCollector(ws); started {
			infraParts = append(infraParts, color.New(color.FgGreen).Sprint("otel: the collector was down, and devstack started it"))
		} else if err != nil {
			infraParts = append(infraParts, color.New(color.FgRed).Sprintf("otel DOWN (devstack can not start it: %v). Run: devstack otel start", err))
		} else {
			infraParts = append(infraParts, color.New(color.FgRed).Sprint("otel is configured, but the collector is DOWN. Run: devstack otel start"))
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
			fmt.Println("  The dev daemon starts. Run 'devstack status' again in a moment.")
		} else {
			fmt.Println("  Run: devstack workspace up")
		}
		return nil
	}

	printServiceOrientation(ws, rw, view)

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
		renderStatusTable(rows, expand)
	}
	if len(condensed) > 0 {
		fmt.Println()
		for _, s := range condensed {
			printCondensedSection(s.color, s.label, s.tag, s.members)
		}
	}
	fmt.Println()

	printFailures(tiltClient, view, ws.Name)

	color.New(color.Faint).Printf("  within a group, top-to-bottom = startup order   ·   blank ENV = no env\n")
	color.New(color.Faint).Printf("  devstack service start <service> --stack base   ·   devstack group start <group> --stack base\n")
	color.New(color.Faint).Printf("  devstack stack up <name>   ·   devstack stack config <svc> --stack <name>\n")
	color.New(color.Faint).Printf("  devstack condenses a group with nothing running, starting, building or erroring   ·   devstack status --all shows every one\n")

	return nil
}

// printFailures says why each erroring copy of this workspace failed. The state
// word alone sent the reader to the logs, and a copy that fails in its run
// command leaves no build record, so the table gave no reason to read at all.
func printFailures(client *tilt.Client, view *tilt.TiltView, wsName string) {
	if view == nil {
		return
	}
	prefix := wsName + ":"
	printed := false
	for _, r := range view.UiResources {
		if !strings.HasPrefix(r.Metadata.Name, prefix) || serviceStatus(r) != "erroring" {
			continue
		}
		reason := client.FailureReason(r)
		if len(reason) == 0 {
			continue
		}
		if !printed {
			color.New(color.FgRed, color.Bold).Printf("  why the erroring copies stopped\n")
			printed = true
		}
		fmt.Printf("  %s\n", r.Metadata.Name)
		for _, line := range reason {
			color.New(color.Faint).Printf("    %s\n", line)
		}
	}
	if printed {
		fmt.Println()
	}
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
			tag += " down"
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

// serviceOrientation is one stack that runs its own copy of the service you are
// standing in.
type serviceOrientation struct {
	stack  string
	state  string
	port   string
	branch string
	note   string
	here   bool
}

// printServiceOrientation answers "where am I and what else is in flight on this
// service" before the workspace table answers "what runs everywhere".
//
// An agent opening a session in a service repo needs the landscape for THAT
// service: which feature stacks run their own copy of it, what each one is for,
// and whether the checkout it is looking at is base or one of them. The
// workspace table lists every service flat, which does not answer any of that.
// Nothing prints when the working directory is not inside a service.
func printServiceOrientation(ws *workspace.Workspace, rw *config.ResolvedWorkspace, view *tilt.TiltView) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	identity, err := config.ResolveIdentity(cwd)
	if err != nil || identity.ServiceName == "" {
		return
	}
	service := identity.ServiceName

	hereStack := ""
	if _, rec, derr := stack.DetectFromCwd(); derr == nil && rec != nil {
		hereStack = rec.Name
	}

	recs, err := stack.LoadStore(ws.Name)
	if err != nil {
		return
	}

	var rows []serviceOrientation
	for _, rec := range recs {
		if !containsString(rec.Overlay, service) {
			continue
		}
		row := serviceOrientation{
			stack:  rec.Name,
			state:  "down",
			port:   "-",
			branch: rec.Branch,
			note:   rec.Note,
			here:   rec.Name == hereStack,
		}
		if p, ok := rec.Ports[stack.QualifyPortKey(service, "http")]; ok {
			row.port = fmt.Sprintf(":%d", p)
		}
		if rec.Active && view != nil {
			if r, ok := hostResourceMap(view.UiResources, ws.Name, rec.Name)[service]; ok {
				row.state = serviceStatus(r)
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].stack < rows[j].stack })

	faint := color.New(color.Faint)
	// Not "base": base runs from its replica, and naming this directory base was
	// read as "the code here is the code base runs".
	where := "template checkout"
	if _, rerr := replica.DetectFromCwd(); rerr == nil {
		where = "base replica"
	}
	if hereStack != "" {
		where = "stack " + hereStack
	}
	repoHere := ""
	if svc, ok := rw.Services[service]; ok {
		repoHere = svc.RepoPath
	}
	if hereStack != "" {
		if _, rec, derr := stack.DetectFromCwd(); derr == nil && rec != nil {
			if wt, ok := rec.Worktrees[service]; ok {
				repoHere = wt
			}
		}
	}
	branchHere := ""
	if repoHere != "" {
		if info := gitinfo.ReadAll(map[string]string{repoHere: repoHere})[repoHere]; info.Label() != "" {
			branchHere = "  ·  " + truncateCell(info.Label(), 42)
		}
	}
	fmt.Printf("  %s  %s%s\n",
		faint.Sprint("you are in"),
		color.New(color.Bold).Sprintf("%s  ·  %s", service, where),
		faint.Sprint(branchHere))

	if len(rows) == 0 {
		faint.Printf("%sno feature stack runs %s. Start one: devstack stack create <name> --repos %s\n\n", orientIndent, service, service)
		return
	}

	faint.Printf("%s%d feature stack(s) run their own %s:\n", orientIndent, len(rows), service)
	for _, r := range rows {
		marker := " "
		if r.here {
			marker = "▸"
		}
		stateColor := color.New(color.Faint)
		if r.state == "running" {
			stateColor = color.New(color.FgGreen)
		}
		fmt.Printf("%s%s %-12s ", orientIndent, marker, r.stack)
		stateColor.Printf("%-9s", r.state)
		fmt.Printf("%-8s ", r.port)
		faint.Printf("%-28s", truncateCell(r.branch, 28))
		if r.note != "" {
			fmt.Printf("  %s", firstLine(r.note, colNote))
		}
		fmt.Println()
	}
	faint.Printf("%s▸ = the checkout you are in · each stack has its own worktree: devstack stack list\n", orientIndent)
	fmt.Println()
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// orientIndent aligns the orientation rows under the "you are in" label.
const orientIndent = "              "

// colNote bounds a stack note in the orientation block. A note is free prose and
// routinely a paragraph; the first line of it is the part that identifies the
// feature, and the rest belongs in `devstack stack list`.
const colNote = 58

// firstLine returns the note's opening line, clipped to n. Notes carry sentences
// of caveat after the headline, which would otherwise wrap the table into noise.
func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(clipRunes(strings.TrimSpace(s), n))
}

// clipRunes shortens s to n printed characters. It counts runes, because
// comparing len(s) in bytes and then slicing []rune panics on any string that
// is longer in bytes than in runes — which is every non-ASCII string. A stack
// note or a branch name with an accent in it crashed the command printing it.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	return string(r[:n-1]) + "…"
}
