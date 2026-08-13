package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// treeNode represents a service within an intra-group dependency tree.
type treeNode struct {
	name     string
	children []*treeNode
}

// buildGroupTree builds a dependency forest for the given group members.
// Only intra-group edges (deps whose target is also in members) become
// parent-child relationships. Cross-group deps are left to the caller.
func buildGroupTree(members []string, deps map[string][]string) []*treeNode {
	memberSet := make(map[string]bool, len(members))
	for _, m := range members {
		memberSet[m] = true
	}

	nodes := make(map[string]*treeNode, len(members))
	for _, m := range members {
		nodes[m] = &treeNode{name: m}
	}

	isChild := make(map[string]bool)
	for _, svc := range members {
		for _, dep := range deps[svc] {
			if memberSet[dep] && dep != svc {
				nodes[dep].children = append(nodes[dep].children, nodes[svc])
				isChild[svc] = true
			}
		}
	}

	var roots []*treeNode
	for _, m := range members {
		if !isChild[m] {
			roots = append(roots, nodes[m])
		}
	}
	// Guard against a degenerate cycle leaving no roots.
	if len(roots) == 0 {
		for _, m := range members {
			roots = append(roots, nodes[m])
		}
	}

	sort.Slice(roots, func(i, j int) bool { return roots[i].name < roots[j].name })
	var sortChildren func(*treeNode)
	sortChildren = func(n *treeNode) {
		sort.Slice(n.children, func(i, j int) bool { return n.children[i].name < n.children[j].name })
		for _, c := range n.children {
			sortChildren(c)
		}
	}
	for _, r := range roots {
		sortChildren(r)
	}
	return roots
}

// orderGroupServices returns the group's members in dependency order —
// every service after the in-group dependencies it waits on.
func orderGroupServices(members []string, deps map[string][]string) []string {
	memberSet := make(map[string]bool, len(members))
	for _, m := range members {
		memberSet[m] = true
	}

	ordered := append([]string(nil), members...)
	sort.Strings(ordered)

	waits := make(map[string][]string, len(ordered))
	for _, m := range ordered {
		for _, dep := range deps[m] {
			if memberSet[dep] && dep != m {
				waits[m] = append(waits[m], dep)
			}
		}
	}

	out := make([]string, 0, len(ordered))
	done := make(map[string]bool, len(ordered))
	for len(out) < len(ordered) {
		next := ""
		for _, m := range ordered {
			if done[m] {
				continue
			}
			ready := true
			for _, dep := range waits[m] {
				if !done[dep] {
					ready = false
					break
				}
			}
			if ready {
				next = m
				break
			}
		}
		if next == "" {
			for _, m := range ordered {
				if !done[m] {
					out = append(out, m)
					done[m] = true
				}
			}
			break
		}
		out = append(out, next)
		done[next] = true
	}
	return out
}

func splitHostResource(name, prefix string) (svc, stackNS string, ok bool) {
	return tilt.SplitResourceName(name, prefix)
}

func hostResourceMap(resources []tilt.UIResource, wsName, stackName string) map[string]tilt.UIResource {
	return tilt.ResourceMap(resources, wsName, stackName)
}

// stackInstancesRunning reports what the base count leaves out: how many of the
// workspace's feature-stack instances are up, and which stacks they belong to.
// The header counts base resources only, so without this the table shows
// running rows the header never accounts for.
func stackInstancesRunning(view *tilt.TiltView, wsName string) (running int, stacks []string) {
	if view == nil {
		return 0, nil
	}
	prefix := wsName + ":"
	seen := map[string]bool{}
	for _, r := range view.UiResources {
		_, ns, ok := splitHostResource(r.Metadata.Name, prefix)
		if !ok || ns == "" || serviceStatus(r) != "running" {
			continue
		}
		running++
		if !seen[ns] {
			seen[ns] = true
			stacks = append(stacks, ns)
		}
	}
	sort.Strings(stacks)
	return running, stacks
}

// stackRunningSummary phrases the stack half of the header, naming the stack
// while there is only one to name.
func stackRunningSummary(running int, stacks []string) string {
	switch {
	case running == 0:
		return ""
	case len(stacks) == 1:
		return fmt.Sprintf("%d more in stack %s", running, stacks[0])
	default:
		// "across N stacks" was read as a count of the workspace's stacks. It
		// counts only the ones with something running, which is fewer.
		return fmt.Sprintf("%d more running, in %d stacks — devstack stack list", running, len(stacks))
	}
}

// otelSegment returns the status header's otel segment for the states decidable
// without touching the collector. decided is false only for the enabled but not
// running case, which the caller resolves with an auto-start attempt.
func otelSegment(running, enabled, pluginConfigured bool, plugin, ui string, httpPort, grpcPort int) (text string, decided bool) {
	switch {
	case running:
		if ui == "" {
			return fmt.Sprintf("otel otlp:%d grpc:%d", httpPort, grpcPort), true
		}
		return fmt.Sprintf("otel ui:%s otlp:%d grpc:%d", ui, httpPort, grpcPort), true
	case enabled:
		return "", false
	case pluginConfigured:
		if plugin == "" {
			plugin = "plugin config"
		}
		return fmt.Sprintf("otel: configured (%s) but not enabled — devstack otel config on", plugin), true
	default:
		return "otel: disabled — devstack otel config on", true
	}
}

// hostOtelLine summarises otel across every registered workspace for the global
// status view. running entries are preformatted "<workspace> ui:<port> otlp:<port>" labels.
func hostOtelLine(running, enabled []string) string {
	switch {
	case len(running) > 0:
		return "otel: " + strings.Join(running, ", ")
	case len(enabled) > 0:
		return "otel: enabled for " + strings.Join(enabled, ", ") + " but collector stopped — devstack otel start"
	default:
		return "otel: no collector running — devstack otel config on"
	}
}

// condenseSection reports whether a section renders as a single summary line
// instead of a full table: nothing running, nothing in flight, and the user did
// not ask for everything expanded.
//
// A service that is failing or on its way up keeps its section expanded.
// Collapsing hid the one row worth reading behind a line saying only that
// nothing was up — which is how "the frontend is down" got answered, and how a
// service wedged in `starting` disappeared from the default view entirely.
func condenseSection(running int, expand bool, inFlight bool) bool {
	return running == 0 && !inFlight && !expand
}

// sectionInFlight reports whether any member of a section is failing or is on
// its way up: the states whose row a reader needs and a summary line cannot
// carry.
func sectionInFlight(s serviceSection) bool {
	for _, svc := range s.members {
		r, ok := s.resources[svc]
		if !ok {
			continue
		}
		switch serviceStatus(r) {
		case "erroring", "starting", "building":
			return true
		}
	}
	return false
}

// wrapCommaList joins names into comma-separated lines no wider than width,
// never dropping a name.
func wrapCommaList(names []string, width int) []string {
	if len(names) == 0 {
		return nil
	}
	var lines []string
	cur := ""
	for i, n := range names {
		piece := n
		if i < len(names)-1 {
			piece += ","
		}
		switch {
		case cur == "":
			cur = piece
		case len(cur)+1+len(piece) > width:
			lines = append(lines, cur)
			cur = piece
		default:
			cur += " " + piece
		}
	}
	return append(lines, cur)
}

// printCondensedSection prints a section with nothing running as one line: the
// marker, the section's coloured name, a count or state tag, then the member
// names wrapped under the first name.
//
// The marker says "none up" and not "idle" because "idle" is a word no other
// devstack surface uses, so a reader cannot map it onto any state in the legend.
// One stack read "active" here, "idle" there and "stopped" in the briefing.
func printCondensedSection(hdrColor *color.Color, label, tag string, names []string) {
	head := statusIndent + condensedMarker + label
	suffix := " " + tag + " "
	faint := color.New(color.Faint)
	faint.Print(statusIndent + condensedMarker)
	hdrColor.Print(label)
	faint.Print(suffix)

	startCol := len(head) + len(suffix)
	lines := wrapCommaList(names, condenseWidth-startCol)
	if len(lines) == 0 {
		fmt.Println()
		return
	}
	for i, line := range lines {
		if i > 0 {
			fmt.Print(strings.Repeat(" ", startCol))
		}
		faint.Println(line)
	}
}

const (
	condenseWidth = 100
	statusIndent  = "  "

	// condensedMarker labels a section collapsed because none of its members
	// run. It is not a state a service is in — the members' own states are what
	// the expanded table shows.
	condensedMarker = "none up  "

	colService    = 22
	colServiceMax = 34
	colGroup      = 12
	colState      = 10
	colBranchMax  = 28
	colPorts      = 14
	colEnv        = 7

	ungroupedLabel = "ungrouped"
)

// serviceSection is one band of the status table — a group of the base
// workspace, the ungrouped remainder, or a feature stack's instances — carrying
// everything needed to render its services without another daemon round-trip.
type serviceSection struct {
	label     string
	members   []string
	deps      map[string][]string
	resources map[string]tilt.UIResource
	envs      map[string]string
	dirs      map[string]string
	color     *color.Color
	running   int
	tag       string
	isStack   bool
}

// statusRow is one rendered line of the single status table.
type statusRow struct {
	service    string
	group      string
	state      string
	ports      string
	env        string
	dir        string
	rowColor   *color.Color
	stateColor *color.Color
}

func sectionRank(s serviceSection) int {
	switch {
	case s.isStack:
		return 2
	case s.label == ungroupedLabel:
		return 1
	default:
		return 0
	}
}

// sortSections orders the table's bands: base groups alphabetically, then the
// ungrouped remainder, then feature stacks alphabetically.
func sortSections(sections []serviceSection) []serviceSection {
	out := append([]serviceSection(nil), sections...)
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := sectionRank(out[i]), sectionRank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].label < out[j].label
	})
	return out
}

// partitionSections splits sections into the ones rendered as table rows and
// the ones collapsed to a single idle line, preserving table order.
func partitionSections(sections []serviceSection, expand bool) (table, condensed []serviceSection) {
	for _, s := range sortSections(sections) {
		if condenseSection(s.running, expand, sectionInFlight(s)) {
			condensed = append(condensed, s)
		} else {
			table = append(table, s)
		}
	}
	return table, condensed
}

// assembleRows flattens sections into table rows, keeping each section's rows
// contiguous and ordering services within a section so dependencies come first.
func assembleRows(sections []serviceSection) []statusRow {
	var rows []statusRow
	for _, s := range sections {
		for _, svc := range orderGroupServices(s.members, s.deps) {
			state, stateClr := svcStatusColor(svc, s.resources)
			rows = append(rows, statusRow{
				service:    svc,
				group:      s.label,
				state:      state,
				ports:      svcPortsRaw(svc, s.resources),
				env:        s.envs[svc],
				dir:        s.dirs[svc],
				rowColor:   s.color,
				stateColor: stateClr,
			})
		}
	}
	return rows
}

// countRunning counts how many of the members are up in the given daemon view.
func countRunning(members []string, resources map[string]tilt.UIResource) int {
	n := 0
	for _, m := range members {
		if r, ok := resources[m]; ok && serviceStatus(r) == "running" {
			n++
		}
	}
	return n
}

// renderStatusTable prints every row of the workspace under one header row,
// colouring each row by the group it belongs to while the STATE cell keeps its
// own state colour. Source paths only print when the caller asked for them.
func renderStatusTable(rows []statusRow, showDirs bool) {
	branches := readBranchLabels(rows)

	svcWidth, groupWidth, branchWidth := colService, colGroup, len("BRANCH")
	for _, r := range rows {
		if len(r.service) > svcWidth {
			svcWidth = len(r.service)
		}
		if len(r.group) > groupWidth {
			groupWidth = len(r.group)
		}
		if n := len(branches[r.dir]); n > branchWidth && n <= colBranchMax {
			branchWidth = n
		} else if n > colBranchMax {
			branchWidth = colBranchMax
		}
	}
	if svcWidth > colServiceMax {
		svcWidth = colServiceMax
	}
	faint := color.New(color.Faint)
	faint.Printf("%s%-*s  %-*s  %-*s  %-*s  %-*s  %s\n", statusIndent,
		svcWidth, "SERVICE", groupWidth, "GROUP", colState, "STATE", branchWidth, "BRANCH", colPorts, "PORTS", "ENV")

	for _, r := range rows {
		fmt.Print(statusIndent)
		r.rowColor.Printf("%-*s", svcWidth, fitCell(r.service, svcWidth))
		fmt.Print("  ")
		r.rowColor.Printf("%-*s", groupWidth, r.group)
		fmt.Print("  ")
		r.stateColor.Printf("%-*s", colState, r.state)
		fmt.Print("  ")
		faint.Printf("%-*s", branchWidth, fitCell(branches[r.dir], branchWidth))
		fmt.Print("  ")
		if r.env != "" {
			printPorts(r.ports, colPorts)
			fmt.Print("  ")
			faint.Print(r.env)
		} else {
			printPorts(r.ports, 0)
		}
		fmt.Println()

		if showDirs && r.dir != "" {
			faint.Printf("%s  %s\n", statusIndent, shortDir(r.dir))
		}
	}
}

// truncateCell shortens a value to its column width, keeping a trailing "*"
// where a branch label carries one: uncommitted work is the part of the label
// that changes what you do next.
func fitCell(v string, width int) string {
	r := []rune(v)
	if len(r) <= width {
		return v
	}
	suffix := ".."
	if strings.HasSuffix(v, "*") {
		suffix = "..*"
	}
	if width <= len(suffix) {
		return string(r[:max(width, 0)])
	}
	return string(r[:width-len(suffix)]) + suffix
}

// readBranchLabels reports the checkout label for each row's source directory,
// keyed by that directory so services sharing a repo cost one git call.
func readBranchLabels(rows []statusRow) map[string]string {
	dirs := map[string]string{}
	for _, r := range rows {
		if r.dir != "" {
			dirs[r.dir] = r.dir
		}
	}
	labels := make(map[string]string, len(dirs))
	for dir, info := range gitinfo.ReadAll(dirs) {
		labels[dir] = info.Label()
	}
	return labels
}

// runStatusAll shows a summary table of all registered workspaces.
func runStatusAll() error {
	workspaces, err := workspace.All()
	if err != nil {
		return fmt.Errorf("can not load the workspace registry: %w", err)
	}

	if len(workspaces) == 0 {
		fmt.Println("No workspaces registered. Run: devstack workspace add")
		return nil
	}

	type wsResult struct {
		ws       workspace.Workspace
		status   string
		services string
	}

	results := make([]wsResult, len(workspaces))
	var wg sync.WaitGroup

	for i, ws := range workspaces {
		wg.Add(1)
		go func(idx int, w workspace.Workspace) {
			defer wg.Done()

			r := wsResult{ws: w}

			// Check PID file
			pidFile := workspace.PIDFile(w.Name)
			pidAlive := false
			if pidData, err := os.ReadFile(pidFile); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err == nil {
					pidAlive = isProcessAlive(pid)
				}
			}

			// Probe the one host daemon; every workspace's resources live there.
			apiURL := fmt.Sprintf("http://localhost:%d/api/view", workspace.HostTiltPort)
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(apiURL)
			if err != nil || resp.StatusCode != http.StatusOK {
				if pidAlive {
					r.status = "starting"
				} else {
					r.status = "stopped"
				}
				r.services = "-"
				results[idx] = r
				return
			}
			defer resp.Body.Close()

			tiltClient := tilt.NewClient("localhost", workspace.HostTiltPort)
			view, err := tiltClient.GetView()
			if err != nil {
				r.status = "running"
				r.services = "unknown"
				results[idx] = r
				return
			}

			prefix := w.Name + ":"
			total := 0
			running := 0
			for _, res := range view.UiResources {
				if !strings.HasPrefix(res.Metadata.Name, prefix) {
					continue
				}
				total++
				if res.Status.RuntimeStatus == "ok" {
					running++
				}
			}
			if total == 0 {
				r.status = "down"
				r.services = "0 services"
			} else {
				r.status = "running"
				r.services = fmt.Sprintf("%d services (%d running)", total, running)
			}
			results[idx] = r
		}(i, ws)
	}

	wg.Wait()

	fmt.Printf("All workspaces and their stacks run in one host daemon on :%d, addressable as <workspace>:<service>[:<stack>].\n", workspace.HostTiltPort)

	var otelRunning, otelEnabled []string
	for i := range workspaces {
		w := &workspaces[i]
		switch {
		case isOtelRunning(w):
			otelRunning = append(otelRunning, fmt.Sprintf("%s ui:%s otlp:%d", w.Name, otel.QueryEndpointFor(w), workspace.OTLPHTTPPort))
		case config.ObservabilityEnabled(w.Path):
			otelEnabled = append(otelEnabled, w.Name)
		}
	}
	color.New(color.Faint).Printf("%s\n\n", hostOtelLine(otelRunning, otelEnabled))
	fmt.Printf("%-16s %-36s %-8s %-12s %s\n", "WORKSPACE", "PATH", "PORT", "STATUS", "SERVICES")
	fmt.Println(strings.Repeat("-", 88))
	for _, r := range results {
		path := r.ws.Path
		if len(path) > 34 {
			path = "..." + path[len(path)-31:]
		}
		fmt.Printf("%-16s %-36s %-8d %-12s %s\n",
			r.ws.Name,
			path,
			workspace.HostTiltPort,
			r.status,
			r.services,
		)
		if stacks, serr := stack.List(r.ws.Name); serr == nil {
			for _, s := range stacks {
				fmt.Printf("  └ %-14s %-36s %-8d %s\n", s.Name, "", s.BasePort, s.Status)
			}
		}
	}

	return nil
}

func serviceStatus(r tilt.UIResource) string {
	return tilt.ServiceStatus(r)
}

// extractPorts turns endpoint URLs into compact ":PORT" strings.
func extractPorts(links []tilt.EndpointLink) string {
	if len(links) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(links))
	seen := make(map[string]bool, len(links))
	for _, ep := range links {
		part := ep.URL
		if u, err := url.Parse(ep.URL); err == nil && u.Port() != "" {
			part = ":" + u.Port()
		}
		if seen[part] {
			continue
		}
		seen[part] = true
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

// formatUptime returns a human-readable duration since an RFC3339 timestamp.
// Returns "-" if the timestamp is empty, null, or in the future.
func formatUptime(ts *string) string {
	if ts == nil || *ts == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, *ts)
	if err != nil {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		return "-"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// shortDir shortens a path by replacing the home directory prefix with ~.
func shortDir(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
