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

// splitHostResource decomposes a host-daemon resource name under prefix
// (<workspace>:) into its bare service and stack namespace. ok is false when the
// name belongs to a different workspace. A base-workspace resource yields an
// empty stack namespace.
func splitHostResource(name, prefix string) (svc, stackNS string, ok bool) {
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return rest[:i], rest[i+1:], true
	}
	return rest, "", true
}

// hostResourceMap indexes a host-daemon view by bare service name, keeping only
// the resources of one namespace: the base workspace when stackName is "", or a
// feature stack's <ws>:<svc>:<stack> resources otherwise.
func hostResourceMap(resources []tilt.UIResource, wsName, stackName string) map[string]tilt.UIResource {
	prefix := wsName + ":"
	out := make(map[string]tilt.UIResource, len(resources))
	for _, r := range resources {
		svc, ns, ok := splitHostResource(r.Metadata.Name, prefix)
		if !ok || ns != stackName {
			continue
		}
		out[svc] = r
	}
	return out
}

// otelSegment returns the status header's otel segment for the states decidable
// without touching the collector. decided is false only for the enabled but not
// running case, which the caller resolves with an auto-start attempt.
func otelSegment(running, enabled, pluginConfigured bool, plugin string, uiPort, httpPort, grpcPort int) (text string, decided bool) {
	switch {
	case running:
		return fmt.Sprintf("otel ui:%d otlp:%d grpc:%d", uiPort, httpPort, grpcPort), true
	case enabled:
		return "", false
	case pluginConfigured:
		if plugin == "" {
			plugin = "plugin config"
		}
		return fmt.Sprintf("otel: configured (%s) but not enabled — devstack otel enable", plugin), true
	default:
		return "otel: disabled — devstack otel enable", true
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
		return "otel: no collector running — devstack otel enable"
	}
}

// condenseSection reports whether a section renders as a single summary line
// instead of a full table: nothing running, and the user did not ask for
// everything expanded.
func condenseSection(running int, expand bool) bool {
	return running == 0 && !expand
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

// printCondensedSection prints a section with nothing running as one line: its
// coloured header, a count or state tag, then the member names wrapped under the
// first name.
func printCondensedSection(hdrColor *color.Color, label, tag string, names []string) {
	head := "● " + label
	suffix := "  " + tag + "  "
	hdrColor.Print(head)
	faint := color.New(color.Faint)
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

	colService    = 22
	colServiceMax = 34
	colState      = 10
	colPorts      = 14
	colEnv        = 7
	colNeeds      = 36
)

// commonEnv returns the group's prevailing environment — the one most of its
// services resolve to. It is shown once on the group header so only the
// services that differ have to name their own; ties break alphabetically for
// stable output. Returns "" when no service in the group has an env.
func commonEnv(members []string, svcEnvNames map[string]string) string {
	counts := map[string]int{}
	for _, m := range members {
		if e := svcEnvNames[m]; e != "" {
			counts[e]++
		}
	}
	names := make([]string, 0, len(counts))
	for e := range counts {
		names = append(names, e)
	}
	sort.Strings(names)

	best := ""
	for _, e := range names {
		if counts[e] > counts[best] {
			best = e
		}
	}
	return best
}

// renderServiceRows prints one row per service in dependency order, listing the
// deps each service waits on. memberSet is the set of services in the current
// section; deps outside it keep their own group colour. groupEnv is the env
// shared by the whole section, which rows omit.
func renderServiceRows(order []string, resourceMap map[string]tilt.UIResource, deps map[string][]string, memberSet map[string]bool, svcGroupColor map[string]*color.Color, serviceDirs map[string]string, svcEnvNames map[string]string, groupEnv string) {
	const indent = "  "
	width := colService
	for _, svc := range order {
		if len(svc) > width {
			width = len(svc)
		}
	}
	if width > colServiceMax {
		width = colServiceMax
	}
	needsIndent := strings.Repeat(" ", len(indent)+width+2+colState+2+colPorts+2+colEnv+2)

	faint := color.New(color.Faint)
	faint.Printf("%s%-*s  %-*s  %-*s  %-*s  %s\n", indent,
		width, "SERVICE", colState, "STATE", colPorts, "PORTS", colEnv, "ENV", "NEEDS")

	for _, svc := range order {
		statusStr, statusClr := svcStatusColor(svc, resourceMap)

		env := svcEnvNames[svc]
		if env == groupEnv {
			env = ""
		}
		hasNeeds := len(deps[svc]) > 0

		fmt.Print(indent)
		fmt.Printf("%-*s  ", width, svc)
		statusClr.Printf("%-*s", colState, statusStr)
		fmt.Print("  ")
		if hasNeeds || env != "" {
			printPorts(svcPortsRaw(svc, resourceMap), colPorts)
		} else {
			printPorts(svcPortsRaw(svc, resourceMap), 0)
		}

		switch {
		case hasNeeds:
			fmt.Print("  ")
			faint.Printf("%-*s", colEnv, env)
			fmt.Print("  ")
			printNeeds(deps[svc], memberSet, svcGroupColor, needsIndent)
		case env != "":
			fmt.Print("  ")
			faint.Print(env)
		}
		fmt.Println()

		if dir := serviceDirs[svc]; dir != "" {
			faint.Printf("%s  %s\n", indent, shortDir(dir))
		}
	}
}

// printNeeds prints a service's dependencies, wrapping onto continuation lines
// aligned under the NEEDS column.
func printNeeds(svcDeps []string, memberSet map[string]bool, svcGroupColor map[string]*color.Color, contIndent string) {
	names := append([]string(nil), svcDeps...)
	sort.Strings(names)

	faint := color.New(color.Faint)
	col, first := 0, true
	for _, dep := range names {
		switch {
		case first:
		case col+2+len(dep) > colNeeds:
			faint.Print(",")
			fmt.Println()
			fmt.Print(contIndent)
			col, first = 0, true
		default:
			faint.Print(", ")
			col += 2
		}
		if c, ok := svcGroupColor[dep]; ok && !memberSet[dep] {
			c.Print(dep)
		} else {
			faint.Print(dep)
		}
		col += len(dep)
		first = false
	}
}

// runStatusAll shows a summary table of all registered workspaces.
func runStatusAll() error {
	workspaces, err := workspace.All()
	if err != nil {
		return fmt.Errorf("failed to load workspace registry: %w", err)
	}

	if len(workspaces) == 0 {
		fmt.Println("No workspaces registered. Run: devstack register")
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
			active := 0
			for _, res := range view.UiResources {
				if !strings.HasPrefix(res.Metadata.Name, prefix) {
					continue
				}
				total++
				if res.Status.RuntimeStatus == "ok" {
					active++
				}
			}
			if total == 0 {
				r.status = "inactive"
				r.services = "0 services"
			} else {
				r.status = "running"
				r.services = fmt.Sprintf("%d services (%d active)", total, active)
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
			otelRunning = append(otelRunning, fmt.Sprintf("%s ui:%d otlp:%d", w.Name, w.UIPort(), w.HTTPPort()))
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

// serviceStatus derives a human-readable status from Tilt resource state.
func serviceStatus(r tilt.UIResource) string {
	if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
		return "disabled"
	}
	switch r.Status.RuntimeStatus {
	case "ok":
		return "running"
	case "pending":
		return "starting"
	case "error":
		return "error"
	}
	if r.Status.UpdateStatus == "running" {
		return "building"
	}
	if r.Status.UpdateStatus == "error" {
		return "error"
	}
	return "idle"
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
