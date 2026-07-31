package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/hooks"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/observability"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tunnel"
	"github.com/socialviolation/devstack/internal/workspace"
)

// errorRegex matches common error-indicating log keywords.
var errorRegex = regexp.MustCompile(`(?i)(error|exception|panic|fatal|fail)`)

// RegisterTools registers devstack MCP tools: status, topology, start, restart,
// stop, configure, process_logs, service_env, observability, stack and env tools,
// plus investigate when observability is enabled and tunnel when an ssh client
// is available.
func RegisterTools(
	mcpServer *server.MCPServer,
	tiltClient *tilt.Client,
	defaultService string,
	backend observability.Backend,
	workspaceName string,
	workspacePath string,
	ws *workspace.Workspace,
) {
	var obsURL string
	if ws != nil {
		obsURL = otel.QueryEndpointFor(ws)
	}

	// The environment orientation tool is always available — it tells the agent
	// which of the capability-gated tools below exist for this context.
	registerEnvironmentTool(mcpServer, obsURL, workspaceName, workspacePath, defaultService, ws)

	cfg, _ := config.Load(workspacePath)
	if cfg == nil {
		cfg = &config.WorkspaceConfig{
			Deps:         map[string][]string{},
			Groups:       map[string][]string{},
			ServicePaths: map[string]string{},
		}
	}
	serviceDirs := cfg.ServicePaths
	registerStatusTool(mcpServer, tiltClient, serviceDirs, cfg, ws)
	registerTopologyTool(mcpServer, workspacePath)
	registerStartTool(mcpServer, tiltClient, defaultService, cfg, ws)
	registerRestartTool(mcpServer, tiltClient, defaultService, cfg, ws)
	registerStopTool(mcpServer, tiltClient, defaultService, cfg, ws)
	registerConfigureTool(mcpServer, tiltClient, ws)
	registerProcessLogsTool(mcpServer, tiltClient, defaultService, cfg, ws)
	registerServiceEnvTool(mcpServer, ws, workspacePath)

	// Observability control (status/enable/disable/configure) is always
	// available so an agent can discover and turn it on.
	registerObservabilityTool(mcpServer, ws, workspacePath)

	// The trace-query tool only makes sense when the workspace has opted into
	// observability — otherwise there is no collector or backend to query.
	// (Telemetry evidence/confidence lives in the observability tool's status.)
	if config.ObservabilityEnabled(workspacePath) {
		registerInvestigateTool(mcpServer, tiltClient, defaultService, backend, obsURL, workspacePath, ws)
	}

	// Tunnels are plain ssh forwards to any reachable host or ssh-config alias
	// (internal/tunnel shells out to ssh and nothing else), so the gate is ssh
	// itself — a tailnet is one way to reach the remote, not a requirement.
	if coreSSHAvailable() {
		registerTunnelTool(mcpServer, tiltClient, ws)
	}

	// Feature stacks overlay this workspace as their base.
	registerStackTools(mcpServer, ws)

	// Lifecycle hooks: discovery and manual re-run. The lifecycle tools above
	// fire their own events; this is how an agent sees what is wired up.
	registerHooksTool(mcpServer, ws)

	// Config-patch environments: point scopes at named envs and inspect them.
	registerEnvTools(mcpServer, ws, workspacePath)
}

// coreSSHAvailable reports whether the ssh client is on PATH — every tunnel
// action that touches a remote is an ssh child process.
func coreSSHAvailable() bool {
	_, err := exec.LookPath("ssh")
	return err == nil
}

// mcpServiceStatus derives a human-readable status string from Tilt resource state.
func mcpServiceStatus(r tilt.UIResource) string {
	if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
		return "disabled"
	}
	switch r.Status.RuntimeStatus {
	case "ok":
		return "running"
	case "pending":
		return "starting"
	case "error":
		return "erroring"
	}
	if r.Status.UpdateStatus == "running" {
		return "building"
	}
	if r.Status.UpdateStatus == "error" {
		return "erroring"
	}
	return "stopped"
}

// mcpExtractPorts returns compact ":PORT" strings from endpoint links.
func mcpExtractPorts(links []tilt.EndpointLink) string {
	if len(links) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(links))
	for _, ep := range links {
		// Extract just the port from the URL
		u := ep.URL
		if i := strings.LastIndex(u, ":"); i != -1 {
			port := strings.TrimRight(u[i:], "/")
			// Sanity check: port should be short and numeric after the colon
			if len(port) <= 6 {
				parts = append(parts, port)
				continue
			}
		}
		parts = append(parts, u)
	}
	return strings.Join(parts, " ")
}

// serviceGroup returns the group name for a given service, or "" if ungrouped.
func serviceGroup(svcName string, cfg *config.WorkspaceConfig) string {
	for groupName, members := range cfg.Groups {
		for _, m := range members {
			if m == svcName {
				return groupName
			}
		}
	}
	return ""
}

// sortedGroupKeys returns sorted keys of the groups map.
func sortedGroupKeys(groups map[string][]string) []string {
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// availableGroups returns a sorted comma-separated list of group names.
func availableGroups(cfg *config.WorkspaceConfig) string {
	keys := sortedGroupKeys(cfg.Groups)
	if len(keys) == 0 {
		return "(none)"
	}
	return strings.Join(keys, ", ")
}

func registerStatusTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, serviceDirs map[string]string, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("status",
		mcp.WithDescription("Show the current status of all services in the LOCAL dev stack. Status reflects the current state of locally running dev services, not production. Returns SERVICE, STATUS (one of running/starting/building/stopped/erroring/disabled/unknown), PORT(S), PATH (source directory), BRANCH (the git branch that directory is on, with * for uncommitted changes — this is the code the process is running), GROUP, ENV (the active environment/config-patch the instance is pointed at, blank if none), and last error. Also shows a groups summary. 'running' means the process is up. 'starting' means it is coming up; 'building' means the daemon is building/updating it. 'stopped' means the service is known but not currently running (not started yet, or was stopped). 'erroring' means the service or its build failed — check logs. 'disabled' means the resource is switched off in the daemon. 'unknown' means the daemon reported no state for it. Pass stack to see a feature stack's instances. RELOAD says whether a service reloads on its own (auto) or needs an explicit restart after an edit (manual)."),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient, serviceDirs: serviceDirs, cfg: cfg}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		serviceDirs, cfg := t.serviceDirs, t.cfg

		view, err := t.client.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if len(view.UiResources) == 0 {
			return mcp.NewToolResultText(targetHeader(t.label) + "Tilt is running but no services are loaded yet. It may still be starting up."), nil
		}

		// Build a map of service name -> status for the groups summary.
		svcStatus := make(map[string]string, len(view.UiResources))

		rw, _ := config.ResolveWorkspace(ws.Path)
		wsEnv := ""
		if rw != nil {
			wsEnv = rw.Manifest.Workspace.Env
		}
		stackEnv := ""
		if t.namespace != "" {
			if rec, err := stack.FindStack(ws.Name, t.namespace); err == nil && rec != nil {
				stackEnv = rec.Env
			}
		}

		checkouts := gitinfo.ReadAll(serviceDirs)

		rows := [][]string{{"SERVICE", "STATUS", "PORT(S)", "PATH", "BRANCH", "GROUP", "ENV", "RELOAD", "ERROR"}}
		prefix := ws.Name + ":"
		for _, r := range view.UiResources {
			svc, stackNS, ok := splitHostResource(r.Metadata.Name, prefix)
			if !ok || stackNS != t.namespace {
				continue
			}
			name := svc
			status := mcpServiceStatus(r)
			svcStatus[name] = status
			ports := mcpExtractPorts(r.Status.EndpointLinks)
			lastError := ""
			if len(r.Status.BuildHistory) > 0 {
				lastError = r.Status.BuildHistory[0].Error
			}
			if len(lastError) > 50 {
				lastError = lastError[:47] + "..."
			}
			path := shortenPath(serviceDirs[svc])
			group := serviceGroup(name, cfg)
			svcEnv := ""
			reload := coreReloadUnknown
			if rw != nil {
				if rs, ok := rw.Services[svc]; ok {
					if rs.Manifest != nil {
						svcEnv = rs.Manifest.Service.Env
					}
					reload = coreReloadMode(rs.Manifest, rs.RepoPath)
				}
			}
			env := config.ActiveEnvName(wsEnv, svcEnv, stackEnv)
			if env == "" {
				env = "-"
			}
			branch := checkouts[svc].Label()
			if branch == "" {
				branch = "-"
			}
			rows = append(rows, []string{name, status, ports, path, branch, group, env, reload, lastError})
		}

		var sb strings.Builder
		sb.WriteString(targetHeader(t.label))
		sb.WriteString("Tilt is running.\n\n")
		sb.WriteString(renderColumns(rows))
		sb.WriteString("\nBRANCH is the git checkout each service runs from; * marks uncommitted changes.\nA service runs the code on that branch — if it is not the branch you expect, the running process does not contain the work you are looking for.\nRELOAD auto = source edits apply on their own; manual = restart it after editing or it keeps running the old code.\n")

		// Groups summary section.
		if len(cfg.Groups) > 0 {
			sb.WriteString("\ngroups:\n")
			for _, groupName := range sortedGroupKeys(cfg.Groups) {
				members := cfg.Groups[groupName]
				healthy := 0
				parts := make([]string, 0, len(members))
				for _, m := range members {
					st := svcStatus[m]
					if st == "" {
						st = "unknown"
					}
					parts = append(parts, fmt.Sprintf("%s(%s)", m, st))
					if st == "running" {
						healthy++
					}
				}
				fmt.Fprintf(&sb, "  %s: %s — %d/%d healthy\n", groupName, strings.Join(parts, ", "), healthy, len(members))
			}
		}

		if config.ObservabilityEnabled(ws.Path) && !otel.CollectorRunning() {
			sb.WriteString("\n⚠ observability is enabled for this workspace but the collector is NOT running — telemetry is not being captured. Start it: devstack otel start\n")
		}

		if t.label == "" {
			if footer := otherStacksFooter(ws); footer != "" {
				sb.WriteString(footer)
			}
		}

		return mcp.NewToolResultText(sb.String()), nil
	})
}

// otherStacksFooter notes the workspace's in-flight feature stacks so an agent
// working on the base sees that other versions exist. Empty when the workspace
// has no stacks.
func otherStacksFooter(ws *workspace.Workspace) string {
	if ws == nil {
		return ""
	}
	stacks, err := stack.List(ws.Name)
	if err != nil || len(stacks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(stacks))
	for _, s := range stacks {
		parts = append(parts, fmt.Sprintf("%s(%s, base :%d)", s.Name, s.Status, s.BasePort))
	}
	return fmt.Sprintf("\nfeature stacks of %s: %s\n", ws.Name, strings.Join(parts, ", "))
}

// maxColWidth caps how wide any one column may grow, so a single verbose cell
// (a long endpoint URL, say) cannot crowd out the rest of the table.
const maxColWidth = 40

// renderColumns lays out rows (the first being the header) as a fixed-width
// table sized to its own content, with a rule under the header. Columns are
// sized from the widest cell, capped at maxColWidth, so a long path or branch
// name cannot push the rest of the row out of alignment; the last column is
// neither padded nor truncated.
func renderColumns(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len([]rune(cell)) > widths[i] {
				widths[i] = min(len([]rune(cell)), maxColWidth)
			}
		}
	}

	var sb strings.Builder
	for r, row := range rows {
		for i, cell := range row {
			if i == len(row)-1 {
				sb.WriteString(cell)
				continue
			}
			runes := []rune(cell)
			if len(runes) > widths[i] {
				runes = append(runes[:widths[i]-1:widths[i]-1], '…')
			}
			sb.WriteString(string(runes))
			sb.WriteString(strings.Repeat(" ", widths[i]-len(runes)+2))
		}
		sb.WriteString("\n")
		if r == 0 {
			total := len(widths) - 1
			for _, w := range widths {
				total += w + 2
			}
			sb.WriteString(strings.Repeat("-", total) + "\n")
		}
	}
	return sb.String()
}

// shortenPath replaces the home directory prefix with ~ for readability.
func shortenPath(path string) string {
	if path == "" {
		return "-"
	}
	home := os.Getenv("HOME")
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// coreCSV renders a list for topology output, or "-" when it is empty.
func coreCSV(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

// coreRenderTopology renders the declared service graph, or one service's node
// when service is set.
func coreRenderTopology(graph *config.TopologyGraph, service string) (string, error) {
	writeService := func(sb *strings.Builder, s *config.ServiceTopology) {
		fmt.Fprintf(sb, "  - %s\n", s.Name)
		fmt.Fprintf(sb, "      path: %s\n", s.Path)
		fmt.Fprintf(sb, "      groups: %s\n", coreCSV(s.Groups))
		fmt.Fprintf(sb, "      dependencies: %s\n", coreCSV(s.Dependencies))
		fmt.Fprintf(sb, "      dependents: %s\n", coreCSV(s.Dependents))
		fmt.Fprintf(sb, "      calls: %s\n", coreCSV(s.Calls))
		fmt.Fprintf(sb, "      called by: %s\n", coreCSV(s.CalledBy))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "workspace: %s (%s)\n\n", graph.WorkspaceName, graph.WorkspaceRoot)

	if service != "" {
		s, ok := graph.Services[service]
		if !ok {
			return "", fmt.Errorf("service %q not found in workspace %q. Known services: %s", service, graph.WorkspaceName, coreCSV(graph.ServiceNames()))
		}
		sb.WriteString("Service:\n")
		writeService(&sb, s)
	} else {
		if len(graph.Groups) == 0 {
			sb.WriteString("Groups: -\n")
		} else {
			sb.WriteString("Groups:\n")
			for _, group := range graph.GroupNames() {
				fmt.Fprintf(&sb, "  - %s: %s\n", group, coreCSV(graph.Groups[group]))
			}
		}
		sb.WriteString("\nServices:\n")
		for _, name := range graph.ServiceNames() {
			writeService(&sb, graph.Services[name])
		}
	}

	if len(graph.Issues) > 0 {
		sb.WriteString("\nIssues:\n")
		for _, issue := range graph.Issues {
			fmt.Fprintf(&sb, "  - [%s] %s\n", issue.Severity, issue.Message)
		}
	}

	sb.WriteString("\nThis is declared configuration, not runtime state — use status for what is running.\n")
	return sb.String(), nil
}

func registerTopologyTool(mcpServer *server.MCPServer, workspacePath string) {
	tool := mcp.NewTool("topology",
		mcp.WithDescription("Show this workspace's declared service graph: every service, its source directory, its groups, the services it depends on, the services that depend on it, and the call edges recorded for it — plus config issues such as unknown group members, unknown dependencies and dependency cycles. Read this before claiming that one service calls or depends on another; the graph comes from the workspace manifest, not from guessing at code. Pass service to show one service's node alone. Reflects declared configuration, not runtime state — use status for what is running."),
		mcp.WithString("service",
			mcp.Description("Exact service name to show alone (for example 'api-service'). NOT a description or partial match. If omitted, the whole graph is returned."),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		graph, err := config.BuildTopology(workspacePath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := coreRenderTopology(graph, request.GetString("service", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	})
}

// coreMaxWaitSeconds caps wait_seconds so a wait can never block a session
// indefinitely, and corePollInterval is how often it re-reads the daemon.
const (
	coreMaxWaitSeconds = 300
	corePollInterval   = 2 * time.Second
)

// coreWaitState is one resource's status during a wait, plus whether the daemon
// has recorded a new deploy for it since the wait's baseline.
type coreWaitState struct {
	name     string
	status   string
	deployed bool
	found    bool
}

// coreSettled reports whether a status is one a wait stops on — anything but
// the two in-flight states.
func coreSettled(status string) bool {
	return status != "building" && status != "starting"
}

// coreResourceSettled reports whether a triggered resource has finished. A
// settled status alone is not enough: a trigger takes a moment to flip the
// resource to building, so a still-running old process would otherwise read as
// a completed restart. States the daemon will not move on from are final.
func coreResourceSettled(s coreWaitState) bool {
	// A resource missing from the view has not settled: a restart regenerates
	// the Tiltfile, so a resource can vanish mid-reload, and its absent deploy
	// time otherwise reads as a fresh one.
	if !s.found {
		return false
	}
	switch s.status {
	case "erroring", "disabled", "unknown":
		return true
	}
	return coreSettled(s.status) && s.deployed
}

// coreDeployTimes maps resource name to the daemon's last deploy timestamp — the
// value a completed restart advances.
func coreDeployTimes(view *tilt.TiltView) map[string]string {
	times := make(map[string]string, len(view.UiResources))
	for _, r := range view.UiResources {
		if r.Status.LastDeployTime != nil {
			times[r.Metadata.Name] = *r.Status.LastDeployTime
		}
	}
	return times
}

// coreWaitForSettled polls fetch every interval until every named resource has
// redeployed and left building/starting, or timeout elapses. baseline holds each
// resource's deploy time from before the trigger. It returns the last states
// read and whether they all settled; sleep is injected so the loop is
// exercisable without a daemon.
func coreWaitForSettled(fetch func() (*tilt.TiltView, error), names []string, baseline map[string]string, timeout, interval time.Duration, sleep func(time.Duration)) ([]coreWaitState, bool, error) {
	if interval <= 0 {
		interval = corePollInterval
	}
	var elapsed time.Duration
	for {
		view, err := fetch()
		if err != nil {
			return nil, false, err
		}
		now := coreDeployTimes(view)
		states := make([]coreWaitState, 0, len(names))
		done := true
		for _, n := range names {
			s := coreWaitState{name: n, status: "unknown"}
			for _, r := range view.UiResources {
				if r.Metadata.Name == n {
					s.status = mcpServiceStatus(r)
					s.found = true
					s.deployed = now[n] != baseline[n]
					break
				}
			}
			states = append(states, s)
			if !coreResourceSettled(s) {
				done = false
			}
		}
		if done || elapsed+interval > timeout {
			return states, done, nil
		}
		sleep(interval)
		elapsed += interval
	}
}

// coreWaitReport renders a wait's outcome. A timeout reports the state each
// resource was still in rather than a verdict.
func coreWaitReport(states []coreWaitState, settled bool, timeout time.Duration) string {
	parts := make([]string, 0, len(states))
	for _, s := range states {
		part := fmt.Sprintf("%s=%s", s.name, s.status)
		if !s.found {
			part = s.name + "=not in the daemon (it may still be reloading)"
		}
		if !settled && s.found && !s.deployed && coreSettled(s.status) {
			part += " (no new deploy yet)"
		}
		parts = append(parts, part)
	}
	if settled {
		return "after waiting: " + strings.Join(parts, ", ")
	}
	return fmt.Sprintf("waited %ds and these had not settled: %s — 'building' can persist (slow or hung build); check logs.",
		int(timeout.Seconds()), strings.Join(parts, ", "))
}

// coreWaitFor blocks for at most seconds (capped at coreMaxWaitSeconds) while
// the named resources finish redeploying, and returns a line describing where
// they ended up. seconds <= 0 returns immediately with no line.
func coreWaitFor(tiltClient *tilt.Client, names []string, baseline map[string]string, seconds int) string {
	if seconds <= 0 || len(names) == 0 || tiltClient == nil {
		return ""
	}
	if seconds > coreMaxWaitSeconds {
		seconds = coreMaxWaitSeconds
	}
	timeout := time.Duration(seconds) * time.Second
	states, settled, err := coreWaitForSettled(tiltClient.GetView, names, baseline, timeout, corePollInterval, time.Sleep)
	if err != nil {
		return "\nwait failed: " + err.Error()
	}
	return "\n" + coreWaitReport(states, settled, timeout)
}

func registerRestartTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("restart",
		mcp.WithDescription("Restart a specific service or all services in a group in the LOCAL dev stack by triggering a rebuild. Operates on local dev services only — service name must be exact. If neither service nor group is given, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("service",
			mcp.Description("Exact service name or configured alias (for example 'api-service'). NOT a description or partial match. If omitted, uses the default service for this repo (unless group is given)."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to restart. All services in the group are restarted in parallel. Cannot be combined with service."),
		),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithNumber("wait_seconds",
			mcp.Description("Wait up to this many seconds for the restarted services to settle, then report the state each ended in. Default 0 returns immediately, before the rebuild has finished. Capped at 300. On timeout it names the state each service was still in rather than claiming success.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")
		waitSeconds := int(request.GetFloat("wait_seconds", 0))

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
		}

		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient, cfg: cfg, defaultSvc: defaultService}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient, defaultService, cfg := t.client, t.defaultSvc, t.cfg

		// Regenerate first so an edit to a manifest is what gets restarted.
		syncNotes := strings.Join(hostdaemon.SyncAndReload(tiltClient), "\n")
		if syncNotes != "" {
			syncNotes += "\n"
		}

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		deployedBefore := coreDeployTimes(view)

		// Group restart.
		if groupName != "" {
			members, ok := cfg.Groups[groupName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("group %q not found — available groups: %s", groupName, availableGroups(cfg))), nil
			}

			type restartResult struct {
				svc string
				out string
				err error
			}
			results := make([]restartResult, len(members))
			var wg sync.WaitGroup
			for i, svc := range members {
				wg.Add(1)
				go func(idx int, svcName string) {
					defer wg.Done()
					target := resourceName(ws.Name, svcName, t.namespace)
					// Enable if disabled.
					for _, r := range view.UiResources {
						if r.Metadata.Name == target && r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
							tiltClient.RunCLI("enable", target) //nolint:errcheck
							break
						}
					}
					out, err := tiltClient.RunCLI("trigger", target)
					results[idx] = restartResult{svc: svcName, out: out, err: err}
				}(i, svc)
			}
			wg.Wait()

			var failures []string
			var successes []string
			for _, r := range results {
				if r.err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", r.svc, r.err))
				} else {
					successes = append(successes, r.svc)
				}
			}
			if len(failures) > 0 {
				return mcp.NewToolResultError(fmt.Sprintf("restarted %d/%d services in group %q: %s\nfailures: %s",
					len(successes), len(members), groupName, strings.Join(successes, ", "), strings.Join(failures, "; "))), nil
			}
			waited := make([]string, 0, len(members))
			for _, svc := range members {
				waited = append(waited, resourceName(ws.Name, svc, t.namespace))
			}
			return mcp.NewToolResultText(syncNotes + onTarget(t.label, fmt.Sprintf("restarted %d services in group %s: %s",
				len(members), groupName, strings.Join(successes, ", "))) + coreWaitFor(tiltClient, waited, deployedBefore, waitSeconds)), nil
		}

		// Single service restart.
		if name == "" {
			name = defaultService
		}
		if name == "" {
			return mcp.NewToolResultError("no service specified and no default service configured for this repo"), nil
		}

		resolved, err := tilt.ResolveService(resourceName(ws.Name, name, t.namespace), view)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// If the resource is disabled, enable it first
		for _, r := range view.UiResources {
			if r.Metadata.Name == resolved && r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
				if out, err := tiltClient.RunCLI("enable", resolved); err != nil {
					return mcp.NewToolResultError(fmt.Sprintf("failed to enable %q: %v\n%s", resolved, err, out)), nil
				}
				break
			}
		}

		out, err := tiltClient.RunCLI("trigger", resolved)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to restart %q: %v\n%s", resolved, err, out)), nil
		}

		return mcp.NewToolResultText(syncNotes + onTarget(t.label, fmt.Sprintf("Restarted service %q.", resolved)) + "\n" + out), nil
	})
}

// coreStartOrder expands each seed service into itself plus its dependencies, in
// start order, unioned across seeds with duplicates dropped.
func coreStartOrder(cfg *config.WorkspaceConfig, seeds []string) ([]string, error) {
	var ordered []string
	seen := map[string]bool{}
	for _, svc := range seeds {
		resolved, err := config.ResolveDeps(cfg, svc)
		if err != nil {
			return nil, err
		}
		for _, r := range resolved {
			if !seen[r] {
				seen[r] = true
				ordered = append(ordered, r)
			}
		}
	}
	return ordered, nil
}

func registerStartTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("start",
		mcp.WithDescription("Start a service, or every service in a group, in the LOCAL dev stack. Use this on a service that status reports as 'stopped' or 'disabled'; use restart on one that is already running. Dependencies are read from the workspace's dependency graph and started first, in order, so a service's callees come up before it. Operates on local dev services only — service name must be exact. If neither service nor group is given, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("service",
			mcp.Description("Exact service name or configured alias (for example 'api-service'). NOT a description or partial match. If omitted, uses the default service for this repo (unless group is given)."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to start. Every service in the group is started, dependencies first. Cannot be combined with service."),
		),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithNumber("wait_seconds",
			mcp.Description("Wait up to this many seconds for the started services to settle, then report the state each ended in. Default 0 returns immediately, before startup has finished. Capped at 300. On timeout it names the state each service was still in rather than claiming success.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")
		waitSeconds := int(request.GetFloat("wait_seconds", 0))
		var waited []string

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
		}

		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient, cfg: cfg, defaultSvc: defaultService}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient, defaultService, cfg := t.client, t.defaultSvc, t.cfg

		var seeds []string
		if groupName != "" {
			members, ok := cfg.Groups[groupName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("group %q not found — available groups: %s", groupName, availableGroups(cfg))), nil
			}
			seeds = members
		} else {
			if name == "" {
				name = defaultService
			}
			if name == "" {
				return mcp.NewToolResultError("no service specified and no default service configured for this repo"), nil
			}
			seeds = []string{name}
		}

		ordered, err := coreStartOrder(cfg, seeds)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Regenerate first so an edit to a manifest is what gets started.
		syncNotes := strings.Join(hostdaemon.SyncAndReload(tiltClient), "\n")
		if syncNotes != "" {
			syncNotes += "\n"
		}

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		deployedBefore := coreDeployTimes(view)
		present := map[string]bool{}
		disabled := map[string]bool{}
		for _, r := range view.UiResources {
			present[r.Metadata.Name] = true
			if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
				disabled[r.Metadata.Name] = true
			}
		}

		var started, missing, failures []string
		for _, svc := range ordered {
			rn := resourceName(ws.Name, svc, t.namespace)
			if !present[rn] {
				missing = append(missing, rn)
				continue
			}
			if disabled[rn] {
				if out, err := tiltClient.RunCLI("enable", rn); err != nil {
					failures = append(failures, fmt.Sprintf("%s: enable failed: %v\n%s", svc, err, out))
					continue
				}
			}
			if out, err := tiltClient.RunCLI("trigger", rn); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v\n%s", svc, err, out))
				continue
			}
			started = append(started, svc)
			waited = append(waited, rn)
		}

		if len(started) == 0 && len(failures) == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("nothing to start — the daemon has no resource named %s. Check the name with status.", strings.Join(missing, ", "))), nil
		}

		var sb strings.Builder
		sb.WriteString(syncNotes)
		sb.WriteString(onTarget(t.label, fmt.Sprintf("Started %d service(s) in dependency order: %s.", len(started), strings.Join(started, ", "))))
		if len(missing) > 0 {
			fmt.Fprintf(&sb, "\nnot loaded in the daemon, skipped: %s", strings.Join(missing, ", "))
		}
		if len(failures) > 0 {
			return mcp.NewToolResultError(sb.String() + "\nfailures: " + strings.Join(failures, "; ")), nil
		}
		var hookOut strings.Builder
		hookErr := hooks.Fire(ws, t.namespace, config.EventServiceStart, started, &hookOut)
		sb.WriteString(coreWaitFor(tiltClient, waited, deployedBefore, waitSeconds))
		appendHookOutput(&sb, config.EventServiceStart, hookOut.String(), hookErr)
		if hookErr != nil {
			return mcp.NewToolResultError(sb.String()), nil
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStopTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("stop",
		mcp.WithDescription("Stop (disable) services in the LOCAL dev stack. Operates on local dev services only — service name must be exact. Exactly one target: service stops that service; group stops every service in that group; all=true stops every service of the targeted instance. With none of them, stops the default service for this repo — the same default restart uses (DEVSTACK_DEFAULT_SERVICE), so a bare call never takes the workspace down. Stopping everything requires all=true. Scoped by stack: with stack set, even all=true touches only that stack's instances, never base's."),
		mcp.WithString("service",
			mcp.Description("Exact service name or alias to stop (for example 'api-service'). NOT a description or partial match. If omitted, the default service for this repo is stopped — not every service. Stopping every service requires all=true."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to stop. All services in the group are stopped in parallel, in the targeted instance only. Cannot be combined with service or all."),
		),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithBoolean("all",
			mcp.Description("Stop every service of the targeted instance (the whole workspace, or the whole stack when stack is set). Required to stop more than one service — omitting service/group does NOT mean all. Cannot be combined with service or group.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")
		stopAll := request.GetBool("all", false)

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
		}
		if stopAll && (name != "" || groupName != "") {
			return mcp.NewToolResultError("all cannot be combined with service or group"), nil
		}

		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient, cfg: cfg, defaultSvc: defaultService}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient, defaultService, cfg := t.client, t.defaultSvc, t.cfg

		if name == "" && groupName == "" && !stopAll {
			name = defaultService
			if name == "" {
				return mcp.NewToolResultError("no service specified and no default service configured for this repo — pass service, group, or all=true to stop every service"), nil
			}
		}

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Group stop.
		if groupName != "" {
			members, ok := cfg.Groups[groupName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("group %q not found — available groups: %s", groupName, availableGroups(cfg))), nil
			}

			type stopResult struct {
				svc string
				out string
				err error
			}
			results := make([]stopResult, len(members))
			var wg sync.WaitGroup
			for i, svc := range members {
				wg.Add(1)
				go func(idx int, svcName string) {
					defer wg.Done()
					out, err := tiltClient.RunCLI("disable", resourceName(ws.Name, svcName, t.namespace))
					results[idx] = stopResult{svc: svcName, out: out, err: err}
				}(i, svc)
			}
			wg.Wait()

			var failures []string
			var successes []string
			for _, r := range results {
				if r.err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", r.svc, r.err))
				} else {
					successes = append(successes, r.svc)
				}
			}
			if len(failures) > 0 {
				return mcp.NewToolResultError(fmt.Sprintf("stopped %d/%d services in group %q: %s\nfailures: %s",
					len(successes), len(members), groupName, strings.Join(successes, ", "), strings.Join(failures, "; "))), nil
			}
			var hookOut strings.Builder
			hookErr := hooks.Fire(ws, t.namespace, config.EventServiceStop, successes, &hookOut)
			var gsb strings.Builder
			gsb.WriteString(onTarget(t.label, fmt.Sprintf("stopped %d services in group %s: %s",
				len(members), groupName, strings.Join(successes, ", "))))
			appendHookOutput(&gsb, config.EventServiceStop, hookOut.String(), hookErr)
			return mcp.NewToolResultText(gsb.String()), nil
		}

		// Single service stop.
		if name != "" {
			resolved, err := tilt.ResolveService(resourceName(ws.Name, name, t.namespace), view)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out, err := tiltClient.RunCLI("disable", resolved)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to stop %q: %v\n%s", resolved, err, out)), nil
			}
			var hookOut strings.Builder
			hookErr := hooks.Fire(ws, t.namespace, config.EventServiceStop, []string{name}, &hookOut)
			var ssb strings.Builder
			ssb.WriteString(onTarget(t.label, fmt.Sprintf("Stopped %q.", resolved)))
			appendHookOutput(&ssb, config.EventServiceStop, hookOut.String(), hookErr)
			return mcp.NewToolResultText(ssb.String()), nil
		}

		// Stop all — only ever reached via an explicit all=true, and scoped to
		// the stack's resources when a stack is targeted.
		targets := stackResourceNames(view, ws.Name, t.namespace)
		var sb strings.Builder
		var failures []string
		for _, name := range targets {
			out, err := tiltClient.RunCLI("disable", name)
			if err != nil {
				failures = append(failures, name)
				fmt.Fprintf(&sb, "FAILED %s: %v\n%s\n", name, err, out)
			} else {
				fmt.Fprintf(&sb, "Stopped %s\n", name)
			}
		}
		if len(failures) > 0 {
			return mcp.NewToolResultError(fmt.Sprintf("Some services failed to stop:\n%s", sb.String())), nil
		}
		var hookOut strings.Builder
		hookErr := hooks.Fire(ws, t.namespace, config.EventServiceStop, nil, &hookOut)
		var asb strings.Builder
		asb.WriteString(onTarget(t.label, fmt.Sprintf("Stopped %d service(s).", len(targets))) + "\n" + sb.String() + coreStillRunning(ws, stopAll, request.GetString("stack", "")))
		appendHookOutput(&asb, config.EventServiceStop, hookOut.String(), hookErr)
		return mcp.NewToolResultText(asb.String()), nil
	})
}

// coreStillRunning names what a stop leaves behind. "Stop everything" is a
// common ask that this tool cannot satisfy on its own: it is scoped to one
// instance, and the daemon itself only goes down from the shell.
func coreStillRunning(ws *workspace.Workspace, stoppedAll bool, stackName string) string {
	if !stoppedAll || ws == nil {
		return ""
	}
	var sb strings.Builder
	if stackName == "" || stackName == "base" {
		if recs, err := stack.LoadStore(ws.Name); err == nil {
			var active []string
			for _, rec := range recs {
				if rec.Active {
					active = append(active, rec.Name)
				}
			}
			if len(active) > 0 {
				fmt.Fprintf(&sb, "\nStill running: feature stack(s) %s — a base-wide stop does not touch them. Stop each with stack=<name> all=true, or stack_down.", strings.Join(active, ", "))
			}
		}
	}
	sb.WriteString("\nThe host daemon itself is still up; it only stops from the shell (devstack workspace down).")
	return sb.String()
}

// coreLogFilters records the filters a process_logs call applied.
type coreLogFilters struct {
	Service      string
	Group        string
	Stack        string
	Lines        int
	Offset       int
	Grep         string
	SinceRestart bool
	ErrorsOnly   bool
}

// coreLogEmptyNote explains an empty process_logs result: which filters produced
// it and how to widen them. Silence from a narrow query reads as "the service is
// fine" unless the query is stated alongside it.
func coreLogEmptyNote(f coreLogFilters) string {
	scope := "every service of this instance"
	switch {
	case f.Service != "":
		scope = "service " + f.Service
	case f.Group != "":
		scope = "group " + f.Group
	}
	stackScope := "base (this workspace's base services)"
	if f.Stack != "" {
		stackScope = "stack " + f.Stack
	}

	applied := []string{
		"scope=" + scope,
		"stack=" + stackScope,
		fmt.Sprintf("lines=%d", f.Lines),
		fmt.Sprintf("offset=%d", f.Offset),
		fmt.Sprintf("since_restart=%v", f.SinceRestart),
		fmt.Sprintf("errors_only=%v", f.ErrorsOnly),
	}
	if f.Grep != "" {
		applied = append(applied, fmt.Sprintf("grep=%q", f.Grep))
	}

	var widen []string
	if f.SinceRestart {
		widen = append(widen, "since_restart=false to include output from before the last restart")
	}
	if f.ErrorsOnly {
		widen = append(widen, "errors_only=false to keep non-error lines")
	}
	if f.Grep != "" {
		widen = append(widen, "drop grep to keep non-matching lines")
	}
	if f.Offset > 0 {
		widen = append(widen, "offset=0 to read the most recent output")
	}
	widen = append(widen, fmt.Sprintf("raise lines above %d", f.Lines))
	if f.Stack == "" {
		widen = append(widen, "stack=<name> to read a feature stack's instance instead of base")
	} else {
		widen = append(widen, "omit stack to read base instead of this stack")
	}
	if f.Service != "" || f.Group != "" {
		widen = append(widen, "omit service/group to read every service of this instance")
	}
	widen = append(widen, "status to check the service is running")

	return fmt.Sprintf("Empty means nothing matched these filters — NOT that the service is healthy or silent.\nFilters applied: %s.\nTo widen: %s.",
		strings.Join(applied, ", "), strings.Join(widen, "; "))
}

func registerProcessLogsTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("process_logs",
		mcp.WithDescription("Fetch raw stdout/stderr from a locally running dev service process. NOT a log search engine — fetches live process output directly from the dev daemon. Parameters are structured: exact service name, integer line count, boolean flags. Natural language queries are NOT accepted. Example: service='api-service' lines=100 since_restart=true. Use for services not instrumented with OTEL or when you need unstructured process output. If no service is given, uses this repo's default service when one is configured, and only fetches every service in parallel when there is none. Supports grep filtering, paging via offset, and since_restart to isolate post-startup output. When group is given, fetches logs from all services in the group concurrently. Cannot specify both service and group."),
		mcp.WithString("service",
			mcp.Description("Exact service name or alias (for example 'api-service'). NOT a description or partial match. If omitted, uses the default service for this repo or fetches all."),
		),
		mcp.WithString("group",
			mcp.Description("Group name. Fetches logs from all services in the group concurrently, prefixed with service name. Cannot be combined with service."),
		),
		mcp.WithNumber("lines",
			mcp.Description("Integer number of lines to return. Defaults to 100."),
		),
		mcp.WithNumber("offset",
			mcp.Description("Skip this many lines from the most recent end before returning `lines`. Use for paging backward: offset=0 gives the last 100 lines, offset=100 gives the 100 lines before that. Defaults to 0."),
		),
		mcp.WithString("grep",
			mcp.Description("Regex pattern to filter lines. Only lines matching this pattern are returned. Use context to include surrounding lines."),
		),
		mcp.WithNumber("context",
			mcp.Description("Number of lines before and after each grep match to include (like grep -C N). Only used when grep is set. Defaults to 0."),
		),
		mcp.WithBoolean("since_restart",
			mcp.Description("If true, return only lines since the last deploy/restart of the service. Uses the dev daemon's deploy timestamp — no heuristics. Defaults to true."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, return only lines matching error/exception/panic/fatal/fail. Defaults to false."),
		),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
		}

		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient, cfg: cfg, defaultSvc: defaultService}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient, defaultService, cfg := t.client, t.defaultSvc, t.cfg

		// Only apply defaultService when no group is specified.
		if name == "" && groupName == "" {
			name = defaultService
		}

		lines := int(request.GetFloat("lines", 100))
		offset := int(request.GetFloat("offset", 0))
		grepPattern := request.GetString("grep", "")
		contextLines := int(request.GetFloat("context", 0))
		sinceRestart := request.GetBool("since_restart", true)
		errorsOnly := request.GetBool("errors_only", false)

		// Compile grep regex if provided.
		var grepRe *regexp.Regexp
		if grepPattern != "" {
			var err error
			grepRe, err = regexp.Compile(grepPattern)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid grep pattern %q: %v", grepPattern, err)), nil
			}
		}

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		emptyNote := func() string {
			return coreLogEmptyNote(coreLogFilters{
				Service:      name,
				Group:        groupName,
				Stack:        t.namespace,
				Lines:        lines,
				Offset:       offset,
				Grep:         grepPattern,
				SinceRestart: sinceRestart,
				ErrorsOnly:   errorsOnly,
			})
		}

		processOutput := func(raw string) string {
			allLines := strings.Split(strings.TrimRight(raw, "\n"), "\n")

			// offset + lines paging: fetch window is [len-offset-lines .. len-offset].
			total := len(allLines)
			end := total - offset
			if end <= 0 {
				return fmt.Sprintf("(offset %d exceeds available %d lines)", offset, total)
			}
			start := end - lines
			if start < 0 {
				start = 0
			}
			allLines = allLines[start:end]

			// errors_only filter.
			if errorsOnly {
				var matched []string
				for _, l := range allLines {
					if errorRegex.MatchString(l) {
						matched = append(matched, l)
					}
				}
				allLines = matched
			}

			// grep filter with optional context.
			if grepRe != nil {
				allLines = applyGrep(allLines, grepRe, contextLines)
			}

			if len(allLines) == 0 {
				return ""
			}
			return strings.Join(allLines, "\n")
		}

		// Build the tilt logs args for a single service.
		// When since_restart is set, use --since=<duration> derived from LastDeployTime.
		// Otherwise use --tail to fetch enough lines for offset+lines paging.
		buildLogArgs := func(svcName string) []string {
			args := []string{"logs"}
			if sinceRestart {
				since := lastDeploySince(view, svcName)
				if since != "" {
					args = append(args, "--since="+since)
				}
			}
			if !sinceRestart {
				fetchLines := (lines + offset) * 3
				if fetchLines < 300 {
					fetchLines = 300
				}
				if fetchLines > 5000 {
					fetchLines = 5000
				}
				args = append(args, fmt.Sprintf("--tail=%d", fetchLines))
			}
			args = append(args, svcName)
			return args
		}

		// Group logs: fetch each service's logs concurrently, interleave with prefix.
		if groupName != "" {
			members, ok := cfg.Groups[groupName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("group %q not found — available groups: %s", groupName, availableGroups(cfg))), nil
			}

			type logResult struct {
				svc string
				out string
				err error
			}
			results := make([]logResult, len(members))
			var wg sync.WaitGroup
			for i, svc := range members {
				wg.Add(1)
				go func(idx int, svcName string) {
					defer wg.Done()
					raw, err := tiltClient.RunCLI(buildLogArgs(resourceName(ws.Name, svcName, t.namespace))...)
					results[idx] = logResult{svc: svcName, out: processOutput(raw), err: err}
				}(i, svc)
			}
			wg.Wait()

			var sb strings.Builder
			anyOutput := false
			for _, r := range results {
				if r.err != nil {
					fmt.Fprintf(&sb, "[%s] error fetching logs: %v\n", r.svc, r.err)
					continue
				}
				if r.out == "" {
					fmt.Fprintf(&sb, "[%s] (no output)\n", r.svc)
					continue
				}
				anyOutput = true
				prefix := fmt.Sprintf("[%s] ", r.svc)
				for _, line := range strings.Split(r.out, "\n") {
					fmt.Fprintf(&sb, "%s%s\n", prefix, line)
				}
			}
			if !anyOutput {
				sb.WriteString("\n" + emptyNote() + "\n")
			}
			return mcp.NewToolResultText(targetHeader(t.label) + sb.String()), nil
		}

		if name != "" {
			resolved, err := tilt.ResolveService(resourceName(ws.Name, name, t.namespace), view)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, err := tiltClient.RunCLI(buildLogArgs(resolved)...)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to get logs for %q: %v\n%s", resolved, err, raw)), nil
			}
			out := processOutput(raw)
			if out == "" {
				return mcp.NewToolResultText(targetHeader(t.label) + fmt.Sprintf("No matching output in %s.\n\n", resolved) + emptyNote()), nil
			}
			return mcp.NewToolResultText(targetHeader(t.label) + out), nil
		}

		// All services in parallel.
		type result struct {
			name string
			out  string
			err  error
		}
		services := stackResourceNames(view, ws.Name, t.namespace)
		results := make([]result, len(services))
		var wg sync.WaitGroup
		for i, svc := range services {
			wg.Add(1)
			go func(idx int, svcName string) {
				defer wg.Done()
				raw, err := tiltClient.RunCLI(buildLogArgs(svcName)...)
				results[idx] = result{name: svcName, out: processOutput(raw), err: err}
			}(i, svc)
		}
		wg.Wait()

		var sb strings.Builder
		anyOutput := false
		for _, r := range results {
			fmt.Fprintf(&sb, "=== %s ===\n", r.name)
			if r.err != nil {
				fmt.Fprintf(&sb, "error fetching logs: %v\n\n", r.err)
			} else if r.out == "" {
				sb.WriteString("(no output)\n\n")
			} else {
				anyOutput = true
				sb.WriteString(r.out)
				sb.WriteString("\n\n")
			}
		}
		if !anyOutput {
			sb.WriteString(emptyNote() + "\n")
		}
		return mcp.NewToolResultText(targetHeader(t.label) + sb.String()), nil
	})
}

// lastDeploySince returns a --since duration string (for example "127s") for the given service,
// derived from its LastDeployTime in the Tilt view. Returns "" if unavailable.
func lastDeploySince(view *tilt.TiltView, svcName string) string {
	for _, r := range view.UiResources {
		if r.Metadata.Name != svcName {
			continue
		}
		if r.Status.LastDeployTime == nil {
			return ""
		}
		t, err := time.Parse(time.RFC3339Nano, *r.Status.LastDeployTime)
		if err != nil {
			return ""
		}
		elapsed := time.Since(t)
		if elapsed < 0 {
			elapsed = 0
		}
		// Add 2s buffer so we do not miss the first lines right at deploy time.
		elapsed += 2 * time.Second
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	}
	return ""
}

// applyGrep filters lines to those matching re, including contextLines lines before/after each match.
func applyGrep(lines []string, re *regexp.Regexp, contextLines int) []string {
	if len(lines) == 0 {
		return nil
	}

	// Mark which lines match.
	matched := make([]bool, len(lines))
	for i, l := range lines {
		matched[i] = re.MatchString(l)
	}

	// Expand to include context.
	include := make([]bool, len(lines))
	for i, m := range matched {
		if !m {
			continue
		}
		start := i - contextLines
		if start < 0 {
			start = 0
		}
		end := i + contextLines
		if end >= len(lines) {
			end = len(lines) - 1
		}
		for j := start; j <= end; j++ {
			include[j] = true
		}
	}

	var out []string
	prevIncluded := true
	for i, l := range lines {
		if include[i] {
			if !prevIncluded && len(out) > 0 {
				out = append(out, "---")
			}
			out = append(out, l)
			prevIncluded = true
		} else {
			prevIncluded = false
		}
	}
	return out
}

func registerConfigureTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, ws *workspace.Workspace) {
	tool := mcp.NewTool("configure",
		mcp.WithDescription("Read or set a dev daemon runtime argument. A Tiltfile only sees an argument if it calls config.parse or config.define_*, and the Tiltfile devstack generates calls neither — so on a devstack-managed daemon a write is refused rather than restarting every service to no effect. Where a Tiltfile does read arguments: call this with no key first to read the ones set, because setting one REPLACES the whole list and silently drops anything not passed again. Use this to change feature flags, modes, or other runtime config. Affected services will restart automatically. "+
			"Boundary: this sets arguments the daemon itself reads when it generates the stack — for config values a service reads, use env_use (point a scope at a named config env), env_set (edit a named env's vars) or service_env (edit one service's vars) instead. "+
			"Setting an argument REPLACES the daemon's entire argument list: this tool sets one key, so every argument set earlier is silently dropped."),
		mcp.WithString("key",
			mcp.Description("The argument key (for example 'env', 'debug', 'profile'). Omit it, with no value, to read the arguments currently set instead of writing one."),
		),
		mcp.WithString("value",
			mcp.Description("The value to set (for example 'production', 'true', 'staging')."),
		),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := request.GetString("key", "")
		value := request.GetString("value", "")

		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient := t.client

		if key == "" {
			if value != "" {
				return mcp.NewToolResultError("value given without key — pass both to set an argument, or neither to read the arguments currently set"), nil
			}
			return mcp.NewToolResultText(coreCurrentArgs(tiltClient)), nil
		}
		if value == "" {
			return mcp.NewToolResultError(fmt.Sprintf("no value for %q — pass value to set it, or omit key to read the current arguments", key)), nil
		}
		if !coreTiltfileReadsArgs() {
			return mcp.NewToolResultError("this daemon's Tiltfile declares no arguments, so setting one would restart services and change nothing. devstack generates that Tiltfile and it never calls config.define_string or config.parse, which is what a Tiltfile needs to read an argument. Configure a service through its env instead: env_use, env_set, or service_env."), nil
		}

		out, err := tiltClient.RunCLI("args", "--", fmt.Sprintf("%s=%s", key, value))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set %s=%s: %v\n%s", key, value, err, out)), nil
		}

		return mcp.NewToolResultText(onTarget(t.label, fmt.Sprintf("Set %s=%s. Affected services will restart automatically.", key, value))), nil
	})
}

// coreOtelUIService resolves the observability UI as a forwardable port, the
// same resolution `devstack tunnel --otel` uses. It is not a daemon resource, so
// it is never discovered — it is only added on request.
func coreOtelUIService(ws *workspace.Workspace) (svc tunnel.Service, reason string, ok bool) {
	plugin := otel.For(ws)
	if plugin == nil {
		return tunnel.Service{}, "no observability backend configured", false
	}
	endpoint := plugin.QueryEndpoint(ws)
	if endpoint == "" {
		return tunnel.Service{}, fmt.Sprintf("backend %q has no local UI to forward — telemetry goes upstream instead", plugin.Name()), false
	}
	port := tunnel.PortFromURL(endpoint)
	if port == 0 {
		return tunnel.Service{}, fmt.Sprintf("can not read a port from the %s UI address %q", plugin.Name(), endpoint), false
	}
	return tunnel.Service{Name: "otel-ui (" + plugin.Name() + ")", Port: port, Runtime: "ok"}, "", true
}

// portLabel renders a forward's ports, naming both ends when they differ. A
// mapped forward reported as a single port would hand back the stack's own
// port, which is the one address the far end must not be told to use.
func portLabel(s tunnel.Service) string {
	if !s.Mapped() {
		return fmt.Sprintf(":%d", s.Port)
	}
	return fmt.Sprintf("far end :%d → here :%d", s.RemotePort, s.Port)
}

// portList renders ports for a one-line summary.
func portList(ports []int) string {
	out := make([]string, len(ports))
	for i, p := range ports {
		out[i] = fmt.Sprintf(":%d", p)
	}
	return strings.Join(out, " ")
}

func registerTunnelTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, ws *workspace.Workspace) {
	tool := mcp.NewTool("tunnel",
		mcp.WithDescription("Forward this workspace's LOCAL service ports to or from a remote host over SSH. A remote machine can then reach the services that run on this dev box.\n\n"+
			"Actions:\n"+
			"  status  what this workspace has forwarded now. Add planned=true for what a push WOULD forward instead. Reads this machine only.\n"+
			"  check   ask the remote host what already holds the ports a push binds. Changes nothing. Run it before reclaim=true, which kills those processes.\n"+
			"  push    expose local ports on the remote over ssh -R. This is the common case.\n"+
			"  pull    bring ports from a source machine to here over ssh -L.\n"+
			"  stop    tear down the forwards of this workspace. Narrow it with service.\n\n"+
			"This needs key-based SSH access to the remote. Passwords do not work. If the keys are absent, the result tells you to run ssh-copy-id.\n"+
			"It forwards only the ports that serve traffic now, and skips a service that is idle or dead.\n"+
			"Any host that you can reach over ssh works, and a plain ssh-config alias is one. A tailnet address is one such host, not a requirement.\n\n"+
			"status and stop read the forwards that run, not what discovery covers now. So they report and tear down the observability UI and the forwards of a stack, whether or not this call asked for them.\n"+
			"After the first push or pull, the remote is remembered, with the direction and the stack mapping. A later `devstack tunnel restart` in a shell then repeats the same thing.\n"),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("One of: list, status, check, push, pull, stop. Read-only, changes nothing: 'list', 'status', 'check' (check does open an ssh session to read the far host's ports, but alters nothing). Writes — they start or kill ssh forwards, and push/pull also save the remote: 'push', 'pull', 'stop'.")),
		mcp.WithString("host",
			mcp.Description("Remote host or SSH config alias (for example 'macbook'). Optional if a default is saved for this workspace.")),
		mcp.WithString("user",
			mcp.Description("SSH user. Optional — falls back to the saved user for this workspace.")),
		mcp.WithString("service",
			mcp.Description("Exact service names to limit to, comma-separated, as printed by action=list (CLI: --service). Optional; default is all serving services, and for 'stop' every forward this workspace has running.")),
		mcp.WithBoolean("reclaim",
			mcp.Description("Push only. Kill whatever already holds these ports on the remote before forwarding. Destructive: it tears down forwards belonging to other stacks, so leave it off unless a push failed to bind and you know the port is yours.")),
		mcp.WithBoolean("stacks",
			mcp.Description("Also forward every active feature stack, each on its OWN allocated port — the far end reaches them at those ports, not the usual ones. Default false. Cannot be combined with as_base.")),
		mcp.WithString("as_base",
			mcp.Description("Put ONE feature stack on base's ports: name the stack, and the far end reaches that stack's instances at the addresses base normally serves, with nothing to reconfigure over there. This is what \"let them test my stack on the usual URLs\" means. Cannot be combined with stacks.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithBoolean("otel",
			mcp.Description("Also forward the observability UI port, so the remote can read this machine's telemetry at the same address you use locally (CLI: --otel). Default false. Ignored with a note when the backend has no local UI.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved for tunnels"), nil
		}
		action := strings.ToLower(request.GetString("action", ""))
		host := request.GetString("host", "")
		user := request.GetString("user", "")

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		filter := map[string]bool{}
		for _, s := range strings.Split(request.GetString("service", ""), ",") {
			if s = strings.TrimSpace(s); s != "" {
				filter[s] = true
			}
		}
		asBase := strings.TrimSpace(request.GetString("as_base", ""))
		wantStacks := request.GetBool("stacks", false)
		if asBase != "" && wantStacks {
			return mcp.NewToolResultError("as_base and stacks ask for different things: as_base puts that one stack on base's ports, stacks forwards every stack on its own ports. Pick one"), nil
		}

		var svcs []tunnel.Service
		var otelNotePrefix string
		if asBase != "" {
			mapped, unmapped, aerr := tunnel.StackOnBasePorts(view, filter, ws.Name, asBase)
			if aerr != nil {
				return mcp.NewToolResultError(aerr.Error()), nil
			}
			svcs = mapped
			if len(unmapped) > 0 {
				otelNotePrefix = fmt.Sprintf("not mapped (no base port to map onto): %s\n", strings.Join(unmapped, ", "))
			}
		} else {
			svcs = tunnel.Discover(view, filter, ws.Name, wantStacks)
		}
		otelNote := otelNotePrefix
		if request.GetBool("otel", false) {
			ui, reason, ok := coreOtelUIService(ws)
			if ok {
				svcs = append(svcs, ui)
			} else {
				otelNote = fmt.Sprintf("warning: otel UI not forwarded — %s.\n", reason)
			}
		}
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Port < svcs[j].Port })

		switch action {
		case "list":
			var sb strings.Builder
			sb.WriteString("Would forward (nothing is up as a result of this):\n")
			for _, s := range svcs {
				state := "not serving"
				if tunnel.Listening(s.Port) {
					state = "serving"
				}
				fmt.Fprintf(&sb, "  %-30s %s  (%s)\n", s.Name, portLabel(s), state)
			}
			return mcp.NewToolResultText(otelNote + sb.String()), nil

		case "status":
			var sb strings.Builder
			if ws.TunnelHost != "" {
				fmt.Fprintf(&sb, "remote: %s@%s\n", ws.TunnelUser, ws.TunnelHost)
			}
			// Every discovered service, plus any port still forwarding that
			// discovery no longer covers, so a live tunnel is never invisible.
			names := map[int]string{}
			var ports []int
			for _, s := range svcs {
				names[s.Port] = s.Name
				ports = append(ports, s.Port)
			}
			for _, port := range tunnel.TrackedPorts(ws.Name) {
				if _, known := names[port]; !known {
					names[port] = "(no longer discovered)"
					ports = append(ports, port)
				}
			}
			sort.Ints(ports)
			for _, port := range ports {
				state := "down"
				if tunnel.IsUp(ws.Name, port) {
					state = "up"
				}
				fmt.Fprintf(&sb, "  [%-4s] %-30s :%d\n", state, names[port], port)
			}
			return mcp.NewToolResultText(otelNote + sb.String()), nil

		case "check":
			// The ports a push binds over there, which for a mapped stack are
			// base's, not the stack's own.
			rhost := host
			if rhost == "" {
				rhost = ws.TunnelHost
			}
			ruser := user
			if ruser == "" {
				ruser = ws.TunnelUser
			}
			if rhost == "" || ruser == "" {
				return mcp.NewToolResultError("no remote host/user given and none saved. Pass host and user (they're remembered after the first successful push)."), nil
			}
			if len(svcs) == 0 {
				return mcp.NewToolResultText(otelNote + "No ports to check right now — nothing is serving. Start the services first."), nil
			}
			ports := make([]int, len(svcs))
			byPort := map[int]string{}
			for i, s := range svcs {
				ports[i] = s.Far()
				byPort[s.Far()] = s.Name
			}
			holders, cerr := tunnel.InspectRemote(ruser, rhost, ports)
			if cerr != nil {
				return mcp.NewToolResultError(cerr.Error()), nil
			}
			var sb strings.Builder
			sb.WriteString(otelNote)
			fmt.Fprintf(&sb, "Ports on %s@%s:\n", ruser, rhost)
			held := 0
			for _, h := range holders {
				if h.Info == "" {
					fmt.Fprintf(&sb, "  %-30s :%-6d free\n", byPort[h.Port], h.Port)
					continue
				}
				held++
				fmt.Fprintf(&sb, "  %-30s :%-6d held by %s\n", byPort[h.Port], h.Port, h.Info)
			}
			if held == 0 {
				sb.WriteString("\nEvery port is free. A push will bind without reclaim.\n")
			} else {
				fmt.Fprintf(&sb, "\n%d port(s) are held. A push needs reclaim=true, which kills those processes — they may belong to a colleague or another stack. Narrow it with service.\n", held)
			}
			return mcp.NewToolResultText(sb.String()), nil

		case "stop":
			// Stop what is forwarding, not what discovery happens to cover now: a
			// forward outlives its service, and the observability UI is never a
			// daemon resource at all. Narrowing to the ones asked for is the only
			// time discovery decides.
			ports := tunnel.TrackedPorts(ws.Name)
			if len(filter) > 0 {
				wanted := map[int]bool{}
				for _, s := range svcs {
					wanted[s.Port] = true
				}
				var kept []int
				for _, port := range ports {
					if wanted[port] {
						kept = append(kept, port)
					}
				}
				ports = kept
			}
			if len(ports) == 0 {
				return mcp.NewToolResultText(otelNote + "No tunnels running for this workspace."), nil
			}
			for _, port := range ports {
				tunnel.KillPort(ws.Name, port)
			}
			return mcp.NewToolResultText(otelNote + fmt.Sprintf("Stopped %d tunnel(s): %s.", len(ports), portList(ports))), nil

		case "push", "pull":
			mode := tunnel.ModePush
			if action == "pull" {
				mode = tunnel.ModePull
			}
			rhost := host
			if rhost == "" {
				rhost = ws.TunnelHost
			}
			if rhost == "" {
				return mcp.NewToolResultError("no remote host given and none saved. Pass host (it is remembered after the first successful push)."), nil
			}
			ruser := user
			if ruser == "" {
				ruser = ws.TunnelUser
			}
			if ruser == "" {
				return mcp.NewToolResultError("no SSH user given and none saved. Pass user (it is remembered after the first successful push)."), nil
			}

			var skipped []tunnel.Service
			if mode == tunnel.ModePush {
				svcs, skipped = tunnel.PartitionServing(svcs)
			}
			if len(svcs) == 0 {
				return mcp.NewToolResultText("No serving ports to forward right now. Start the services first (devstack service start)."), nil
			}

			if cerr := tunnel.CheckConnectivity(ruser, rhost); cerr != nil {
				return mcp.NewToolResultText(fmt.Sprintf(
					"Can't open an SSH session to %s@%s.\nssh: %s\n\ndevstack tunnels use key-based SSH (no passwords). Enable it with:\n  1. ssh %s@%s\n  2. ssh-copy-id %s@%s\n  3. retry this tool.",
					ruser, rhost, cerr, ruser, rhost, ruser, rhost)), nil
			}

			// Remember an explicitly provided remote now that it is known to work.
			if host != "" || user != "" {
				_ = workspace.UpdateTunnelRemote(ws.Name, rhost, ruser)
			}

			reclaim := request.GetBool("reclaim", false)
			if mode == tunnel.ModePush && reclaim {
				// The port to free is the one the forward binds over there, which
				// is base's port for a mapped stack, not the stack's own.
				ports := make([]int, len(svcs))
				for i, s := range svcs {
					ports[i] = s.Far()
				}
				tunnel.ReclaimRemote(ruser, rhost, ports)
			}

			var sb strings.Builder
			sb.WriteString(otelNote)
			fmt.Fprintf(&sb, "%s tunnels → %s@%s\n", strings.ToUpper(action), ruser, rhost)
			for _, s := range skipped {
				fmt.Fprintf(&sb, "  [skip]    %-30s :%d  (not serving)\n", s.Name, s.Port)
			}
			var clashed bool
			var started int
			for _, s := range svcs {
				pid, lerr := tunnel.Launch(ws.Name, mode, ruser, rhost, s.Port, s.Far())
				if lerr != nil {
					clashed = true
					fmt.Fprintf(&sb, "  [FAILED]  %-30s %s  (%v)\n", s.Name, portLabel(s), lerr)
					continue
				}
				started++
				fmt.Fprintf(&sb, "  [started] %-30s %s  (pid %d)\n", s.Name, portLabel(s), pid)
			}
			// Record the shape of what is now up, so a later `devstack tunnel
			// restart` re-establishes this and not the flag defaults.
			if started > 0 {
				_ = workspace.UpdateTunnelForward(ws.Name, workspace.TunnelForward{
					Mode:     string(mode),
					Services: request.GetString("service", ""),
					Stacks:   wantStacks,
					AsBase:   asBase,
					Otel:     request.GetBool("otel", false),
				})
			}
			if clashed && mode == tunnel.ModePush && !reclaim {
				fmt.Fprintf(&sb, "\nA forward fails when something already holds the port on %s. It can be a stale "+
					"forward of your own, or it may belong to another stack — check before retrying with reclaim=true.\n", rhost)
			}
			return mcp.NewToolResultText(sb.String()), nil

		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use list, status, check, push, pull, or stop", action)), nil
		}
	})
}

// resolveInvestigateStack maps the investigate tool's raw stack param to a
// TraceQuery.Stack value. An absent/empty param defaults to "base" — an
// unqualified query means the base instance, not every instance co-mingled.
// "all" (or "*") clears the filter to query every instance. Any other value is
// the stack's short name, passed through unchanged.
func resolveInvestigateStack(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "base"
	case "all", "*":
		return ""
	default:
		return raw
	}
}

// coreInvestigateFilters records the filters an investigate search (mode 2 or 3)
// applied, including how the service was chosen when the caller did not
// name one.
type coreInvestigateFilters struct {
	Service                 string
	ServiceSource           string
	Stack                   string
	Attribute               string
	Value                   string
	SinceMinutes            int
	Limit                   int
	ErrorsOnly              bool
	MatchedBeforeErrorsOnly int
}

// coreInvestigateEmptyNote explains an empty investigate result: which filters
// produced it and how to widen them. The defaults are narrow enough that "no
// traces" otherwise reads as "this service is not instrumented".
func coreInvestigateEmptyNote(f coreInvestigateFilters) string {
	stackScope := "stack " + f.Stack + " only"
	switch f.Stack {
	case "":
		stackScope = "all instances (base and every feature stack)"
	case "base":
		stackScope = "base only (base-workspace services, no feature stack)"
	}

	applied := []string{"workspace=this workspace only (always applied)", "stack=" + stackScope}
	if f.Attribute != "" {
		applied = append(applied, fmt.Sprintf("attribute=%s value=%s", f.Attribute, f.Value))
	}
	if f.Service != "" {
		svc := "service=" + f.Service
		if f.ServiceSource != "" {
			svc += " (" + f.ServiceSource + ")"
		}
		applied = append(applied, svc)
	} else {
		applied = append(applied, "service=(none — every service)")
	}
	applied = append(applied,
		fmt.Sprintf("window=last %d minute(s)", f.SinceMinutes),
		fmt.Sprintf("limit=%d", f.Limit),
		fmt.Sprintf("errors_only=%v", f.ErrorsOnly))

	var widen []string
	widen = append(widen, fmt.Sprintf("raise since_minutes above %d — a short window is the usual reason for an empty result", f.SinceMinutes))
	if f.ErrorsOnly {
		widen = append(widen, "errors_only=false to keep executions whose root span is not an error")
	}
	if f.Stack == "base" {
		widen = append(widen, "stack='all' to include every feature stack's telemetry, or stack='<name>' for one")
	} else if f.Stack != "" {
		widen = append(widen, "stack='all' to include base and the other stacks")
	}
	if f.Service != "" {
		widen = append(widen, fmt.Sprintf("name a different service, or search by attribute+value to span services instead of pinning %s", f.Service))
	}
	widen = append(widen, fmt.Sprintf("raise limit above %d", f.Limit))
	widen = append(widen, "the observability tool's status action to check whether these services are emitting telemetry at all")

	var sb strings.Builder
	sb.WriteString("Empty means nothing matched these filters — NOT that the service is healthy, idle or uninstrumented.\n")
	fmt.Fprintf(&sb, "Filters applied: %s.\n", strings.Join(applied, ", "))
	if f.ErrorsOnly && f.MatchedBeforeErrorsOnly > 0 {
		fmt.Fprintf(&sb, "%d execution(s) matched everything except errors_only — traffic exists, none of it errored.\n", f.MatchedBeforeErrorsOnly)
	}
	fmt.Fprintf(&sb, "To widen: %s.", strings.Join(widen, "; "))
	return sb.String()
}

func registerInvestigateTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, backend observability.Backend, obsURL string, workspacePath string, ws *workspace.Workspace) {
	queryURL := obsURL
	var localPluginName string
	var localPluginHasNoUI bool
	var localPluginUpstream string
	if ws != nil {
		if plugin := otel.Get(ws.OtelPlugin); plugin != nil {
			localPluginName = plugin.Name()
			if endpoint := plugin.QueryEndpoint(ws); endpoint != "" {
				queryURL = endpoint
			} else {
				localPluginHasNoUI = true
				localPluginUpstream = ws.PluginConfig("upstream")
			}
		}
	}

	desc := fmt.Sprintf(
		"Investigate distributed traces in the LOCAL dev environment (backend resolved at server start: %s — the observability tool reports the current one, and can change it). "+
			"Queries this workspace's configured telemetry backend — NOT a natural language search engine. Parameters are structured: exact service names, structured time ranges, and exact attribute key=value pairs. "+
			"Results are always confined to this workspace's telemetry, and in mode 3 an unqualified call narrows to the service the server is running in. "+
			"Modes, and what each one filters on: (1) trace_id/span_id — look up one trace or span by id; every other filter is ignored, including service, stack, since_minutes, limit and errors_only, so the trace comes back whichever instance emitted it and however old it is. "+
			"(2) attribute+value — search by business attribute (for example attribute='portfolio.id' value='123'), filtered by stack, since_minutes, limit and errors_only, and by service when service is given (there is no default service in this mode). "+
			"(3) service — recent executions, filtered by stack, since_minutes, limit, errors_only and service, where an omitted service falls back to this repo's default service. "+
			"Querying a stack that is down is not an error but is rarely useful: it returns only what that stack emitted while it was last up, so an empty result there means it is down, not that it is healthy — stack_list reports which stacks are up. "+
			"One backend holds every workspace and every stack, so results are told apart by resource attributes: devstack.workspace (applied for you, always), devstack.service (the name devstack uses, which often differs from the name the service reports itself as — either matches), devstack.stack (base, or a feature stack's name), devstack.env (the config env that instance runs under). "+
			"In modes 2 and 3, results can be isolated to one stack's service: 'service' pins the service and 'stack' pins the devstack.stack resource attribute (a stack's short name, or 'stack'='base' to select base-workspace services). "+
			"To compare a feature stack against base, run the same query twice with stack='<name>' and stack='base'. Use the observability tool's status action to see which variants are emitting before concluding a service is silent. "+
			"Example: service='api-service' stack='perf' since_minutes=15 errors_only=true. "+
			"Returns an ASCII span tree showing service calls, durations, and errors. Combine with process_logs and status for full debugging context.",
		queryURL,
	)

	tool := mcp.NewTool("investigate",
		mcp.WithDescription(desc),
		mcp.WithString("trace_id",
			mcp.Description("Specific trace ID to look up. If given, every other filter is ignored — including service, stack, since_minutes, limit and errors_only."),
		),
		mcp.WithString("span_id",
			mcp.Description("Specific span ID to look up. Finds the trace containing this span, with every other filter ignored. Ignored itself if trace_id is given."),
		),
		mcp.WithString("service",
			mcp.Description("Exact service name (for example 'api-service'). NOT a description or partial match. Applied in mode 2 (attribute search) and mode 3 (recent executions); only mode 3 falls back to this repo's default service when it is omitted. A trace_id/span_id lookup ignores it and returns every service's spans in that trace."),
		),
		mcp.WithString("stack",
			mcp.Description("Which instance's telemetry to query, via the devstack.stack resource attribute. Applied in mode 2 (attribute search) and mode 3 (recent executions) ONLY — a trace_id/span_id lookup is not stack-filtered and returns the trace whichever instance emitted it. Within those two modes: ABSENT/empty = base only (the base-workspace services — the default an unqualified query means). A stack's short name (for example 'perf') = that stack only. 'all' (or '*') = every instance co-mingled (base + all stacks). Combine with 'service' to pin a single instance's service."),
		),
		mcp.WithString("attribute",
			mcp.Description("Exact attribute key to search by (for example 'portfolio.id', 'user.id', 'process.id'). NOT natural language. Requires value parameter."),
		),
		mcp.WithString("value",
			mcp.Description("Exact value to match for the given attribute (for example '123'). NOT a pattern or description."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Look-back window in minutes (integer). Defaults to 30. Use larger values (for example 60) to search further back."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of executions to expand. Defaults to 5."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, only return executions where the root span has error status. Defaults to false."),
		),
		mcp.WithBoolean("verbose",
			mcp.Description("If true, show all span attributes and full correlated logs. Default false returns compact view."),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if backend == nil {
			// Check if this is a forwarding plugin with no local UI
			if localPluginHasNoUI {
				msg := fmt.Sprintf("No local query UI available with the active OTEL plugin (%s). Telemetry is forwarded to %s. Query it there instead.", localPluginName, localPluginUpstream)
				return mcp.NewToolResultText(msg), nil
			}
			return mcp.NewToolResultError("Observability backend is not configured. Check your environment settings."), nil
		}

		traceID := request.GetString("trace_id", "")
		spanID := request.GetString("span_id", "")
		service := request.GetString("service", "")
		stack := resolveInvestigateStack(request.GetString("stack", ""))
		attribute := request.GetString("attribute", "")
		value := request.GetString("value", "")
		sinceMinutes := int(request.GetFloat("since_minutes", 30))
		limit := int(request.GetFloat("limit", 5))
		errorsOnly := request.GetBool("errors_only", false)
		verbose := request.GetBool("verbose", false)

		opts := formatOptions{Verbose: verbose}
		since := time.Duration(sinceMinutes) * time.Minute

		// Mode 1: specific trace ID or span ID
		if traceID == "" && spanID != "" {
			traces, err := backend.QueryTraces(ctx, observability.TraceQuery{SpanID: spanID})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(traces) == 0 || len(traces[0]) == 0 {
				return mcp.NewToolResultText(fmt.Sprintf("Span %q not found.", spanID)), nil
			}
			traceID = traces[0][0].TraceID
		}
		if traceID != "" {
			traces, err := backend.QueryTraces(ctx, observability.TraceQuery{TraceID: traceID})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(traces) == 0 || len(traces[0]) == 0 {
				return mcp.NewToolResultText(fmt.Sprintf("Trace %q not found.", traceID)), nil
			}
			record := spansToRecord(traces[0])
			otelLogs := backendLogsToInternal(queryLogsFromBackend(ctx, backend, traceID, ""))
			d := executionDetail{record: record, otelLogs: otelLogs}
			if len(otelLogs) == 0 {
				tailLines := 20
				if verbose {
					tailLines = 80
				}
				d.tiltLogs = fetchTiltProcessLogs(tiltClient, record, tailLines, verbose)
			}
			return mcp.NewToolResultText(formatExecutionDetailView(&d, opts)), nil
		}

		// Mode 2: attribute search
		var traceGroups [][]observability.Span
		serviceSource := ""
		if service != "" {
			serviceSource = "given"
		}
		if attribute != "" && value != "" {
			matched, err := backend.QueryTraces(ctx, observability.TraceQuery{
				Attribute: attribute,
				Value:     value,
				Service:   service,
				Stack:     stack,
				Since:     since,
				Limit:     limit * 5,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			traceGroups = matched
		} else {
			// Mode 3: recent executions. Scoping to the service the agent is
			// working in keeps an unqualified call from returning the whole
			// workspace's traffic.
			if service == "" {
				service = defaultService
				serviceSource = "this repo's default service, not asked for"
			}
			if service == "" {
				service = serviceFromCwd()
				serviceSource = "detected from the directory the MCP server runs in, not asked for"
			}
			recent, err := backend.QueryTraces(ctx, observability.TraceQuery{
				Service: service,
				Stack:   stack,
				Since:   since,
				Limit:   limit * 5,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			traceGroups = recent
		}

		// Convert to internal records
		roots := make([]traceRecord, 0, len(traceGroups))
		for _, spans := range traceGroups {
			if len(spans) > 0 {
				roots = append(roots, *spansToRecord(spans))
			}
		}

		matchedBeforeErrorsOnly := len(roots)
		if errorsOnly {
			filtered := roots[:0]
			for _, r := range roots {
				if rs := rootSpan(&r); rs != nil && spanHasError(rs) {
					filtered = append(filtered, r)
				}
			}
			roots = filtered
		}

		if len(roots) == 0 {
			msg := fmt.Sprintf("No executions found in the last %d minute(s).", sinceMinutes)
			if attribute != "" {
				msg = fmt.Sprintf("No executions found where %s=%s in the last %d minute(s).", attribute, value, sinceMinutes)
			} else if service != "" {
				msg = fmt.Sprintf("No executions found for %q in the last %d minute(s).", service, sinceMinutes)
			}
			note := coreInvestigateEmptyNote(coreInvestigateFilters{
				Service:                 service,
				ServiceSource:           serviceSource,
				Stack:                   stack,
				Attribute:               attribute,
				Value:                   value,
				SinceMinutes:            sinceMinutes,
				Limit:                   limit,
				ErrorsOnly:              errorsOnly,
				MatchedBeforeErrorsOnly: matchedBeforeErrorsOnly,
			})
			return mcp.NewToolResultText(msg + "\n\n" + note), nil
		}

		if len(roots) > limit {
			roots = roots[:limit]
		}

		tailLines := 20
		if verbose {
			tailLines = 80
		}
		details := fetchExecutionDetailsViaBackend(ctx, roots, backend, tiltClient, tailLines, verbose)

		sep := "\n" + strings.Repeat("─", 60) + "\n\n"
		var sb strings.Builder
		for i := range details {
			if i > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(formatExecutionDetailView(&details[i], opts))
		}

		return mcp.NewToolResultText(sb.String()), nil
	})
}

// spansToRecord converts a slice of observability.Span to the internal traceRecord type.
func spansToRecord(spans []observability.Span) *traceRecord {
	if len(spans) == 0 {
		return nil
	}
	ts := make([]traceSpan, 0, len(spans))
	for _, s := range spans {
		ts = append(ts, traceSpan{
			TraceID:      s.TraceID,
			SpanID:       s.SpanID,
			ParentSpanID: s.ParentSpanID,
			Service:      s.Service,
			Operation:    s.Operation,
			StartNs:      s.StartTime.UnixNano(),
			DurationNs:   s.DurationNano,
			StatusCode:   s.Status,
			Attrs:        s.Attributes,
		})
	}
	traceID := spans[0].TraceID
	return &traceRecord{TraceID: traceID, Spans: ts}
}

// queryLogsFromBackend queries logs for a trace from the backend.
func queryLogsFromBackend(ctx context.Context, backend observability.Backend, traceID, service string) []observability.LogEntry {
	logs, _ := backend.QueryLogs(ctx, observability.LogQuery{
		TraceID: traceID,
		Service: service,
	})
	return logs
}

// backendLogsToInternal converts observability.LogEntry to internal logEntry.
func backendLogsToInternal(entries []observability.LogEntry) []logEntry {
	result := make([]logEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, logEntry{
			Timestamp: e.Timestamp.UnixNano(),
			Body:      e.Body,
			Service:   e.Service,
			Severity:  e.Severity,
			TraceID:   e.TraceID,
			SpanID:    e.SpanID,
		})
	}
	return result
}

// fetchExecutionDetailsViaBackend fetches full span trees and logs via the backend interface.
func fetchExecutionDetailsViaBackend(ctx context.Context, roots []traceRecord, backend observability.Backend, tiltClient *tilt.Client, tailLines int, verbose bool) []executionDetail {
	details := make([]executionDetail, len(roots))
	var wg sync.WaitGroup
	for i, r := range roots {
		wg.Add(1)
		go func(idx int, traceID string) {
			defer wg.Done()
			d := executionDetail{}

			// Fetch full span tree
			traces, err := backend.QueryTraces(ctx, observability.TraceQuery{TraceID: traceID})
			if err == nil && len(traces) > 0 && len(traces[0]) > 0 {
				d.record = spansToRecord(traces[0])
			} else {
				cp := roots[idx]
				d.record = &cp
			}

			// OTEL correlated logs
			obsLogs := queryLogsFromBackend(ctx, backend, traceID, "")
			d.otelLogs = backendLogsToInternal(obsLogs)

			// If no OTEL logs, fetch Tilt process logs for each involved service
			if len(d.otelLogs) == 0 && tiltClient != nil {
				d.tiltLogs = fetchTiltProcessLogs(tiltClient, d.record, tailLines, verbose)
			}

			details[idx] = d
		}(i, r.TraceID)
	}
	wg.Wait()
	return details
}

// executionDetail holds a fully fetched execution: span tree + correlated logs.
type executionDetail struct {
	record   *traceRecord
	otelLogs []logEntry
	// tiltLogs maps service name → recent process log output (fallback when OTEL logs are empty).
	tiltLogs map[string]string
}

func uniqueServices(r *traceRecord) []string {
	seen := make(map[string]bool)
	var out []string
	for _, sp := range r.Spans {
		if sp.Service != "" && !seen[sp.Service] {
			seen[sp.Service] = true
			out = append(out, sp.Service)
		}
	}
	return out
}

// fetchTiltProcessLogs fetches recent stdout logs from Tilt for services in the trace.
// In compact mode (verbose=false), only fetches logs for services that have at least one error span.
// If no services have error spans in compact mode, returns nil.
func fetchTiltProcessLogs(tiltClient *tilt.Client, record *traceRecord, tailLines int, verbose bool) map[string]string {
	if tiltClient == nil || record == nil {
		return nil
	}

	var services []string
	if verbose {
		services = uniqueServices(record)
	} else {
		// Only fetch for services with at least one error span.
		seen := make(map[string]bool)
		for _, sp := range record.Spans {
			if spanHasError(&sp) && sp.Service != "" && !seen[sp.Service] {
				seen[sp.Service] = true
				services = append(services, sp.Service)
			}
		}
		if len(services) == 0 {
			return nil
		}
	}

	type result struct {
		name string
		out  string
	}
	ch := make(chan result, len(services))
	for _, svc := range services {
		go func(s string) {
			out, _ := tiltClient.RunCLI("logs", fmt.Sprintf("--tail=%d", tailLines), s)
			ch <- result{name: s, out: out}
		}(svc)
	}
	m := make(map[string]string, len(services))
	for range services {
		r := <-ch
		m[r.name] = r.out
	}
	return m
}

// formatExecutionDetailView formats a single executionDetail as a self-contained investigation block.
func formatExecutionDetailView(d *executionDetail, opts formatOptions) string {
	var sb strings.Builder

	// Span tree + OTEL header/logs
	sb.WriteString(formatExecutionView(d.record, d.otelLogs, opts))

	// Tilt process log fallback (when OTEL logs were empty)
	if len(d.otelLogs) == 0 && len(d.tiltLogs) > 0 {
		sb.WriteString("\nPROCESS LOGS (recent stdout — not trace-correlated):\n")
		for svc, raw := range d.tiltLogs {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			fmt.Fprintf(&sb, "--- %s ---\n%s\n", svc, strings.TrimRight(raw, "\n"))
		}
	}

	return sb.String()
}

// serviceFromCwd returns the service whose repo the MCP server is running in,
// or "" when it is not inside one. It narrows an unqualified investigate call to
// the service being worked on.
func serviceFromCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	identity, err := config.ResolveIdentity(cwd)
	if err != nil {
		return ""
	}
	return identity.ServiceName
}

// coreTiltfileReadsArgs reports whether the running Tiltfile declares any
// arguments. Tilt only surfaces an argument to a Tiltfile through config.parse
// or config.define_*, so a Tiltfile with neither ignores everything set on it —
// and devstack generates a Tiltfile with neither.
func coreTiltfileReadsArgs() bool {
	data, err := os.ReadFile(filepath.Join(workspace.HostTiltDir(), "Tiltfile"))
	if err != nil {
		return true
	}
	src := string(data)
	return strings.Contains(src, "config.define_") || strings.Contains(src, "config.parse")
}

// coreCurrentArgs reports the daemon's current Tiltfile arguments. Setting one
// replaces the whole list, so a caller has to be able to read it first; `tilt
// args` with no arguments opens an editor, which would hang the server, so the
// list comes from the API object instead.
func coreCurrentArgs(tiltClient *tilt.Client) string {
	out, err := tiltClient.RunCLI("get", "tiltfiles", "-o", "json")
	if err != nil {
		return fmt.Sprintf("can not read the daemon's arguments: %v\n%s", err, out)
	}

	var payload struct {
		Items []struct {
			Spec struct {
				Args []string `json:"args"`
			} `json:"spec"`
		} `json:"items"`
		Spec struct {
			Args []string `json:"args"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return fmt.Sprintf("can not parse the daemon's arguments: %v", err)
	}

	args := payload.Spec.Args
	for _, item := range payload.Items {
		if len(item.Spec.Args) > 0 {
			args = item.Spec.Args
		}
	}
	if len(args) == 0 {
		return "No arguments are set on the daemon. Setting one replaces the whole list, so with none set there is nothing to preserve.\n"
	}
	return fmt.Sprintf("Arguments currently set on the daemon: %s\nSetting one replaces this whole list — pass every argument you want to keep.\n", strings.Join(args, " "))
}
