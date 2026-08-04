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
	"github.com/socialviolation/devstack/internal/migrate"
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
// stop, configure, process_logs, service_env, observability, migrate, stack and
// env tools, plus investigate when observability is enabled and tunnel when an
// ssh client is available.
//
// patches is the migration set the migrate tool runs. The patches are declared
// where the commands are, so the caller passes them in rather than this package
// importing that one.
func RegisterTools(
	mcpServer *server.MCPServer,
	tiltClient *tilt.Client,
	defaultService string,
	backend observability.Backend,
	workspaceName string,
	workspacePath string,
	ws *workspace.Workspace,
	patches []migrate.Patch,
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
	registerBaseTool(mcpServer, ws)
	registerMigrateTool(mcpServer, patches)

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
		mcp.WithDescription("Show the state of every service copy in the LOCAL dev stack. The state is the state of the local dev services, and not of production.\n"+
			"Columns:\n"+
			"  SERVICE  the service name.\n"+
			"  STATUS   one of running, starting, building, stopped, erroring, disabled, down, unknown.\n"+
			"  PORT(S)  the ports this copy serves on.\n"+
			"  PATH     the directory this copy runs from. For a stack that is the stack's own worktree. For base it is the replica worktree.\n"+
			"  BRANCH   the git branch that this directory is on. A * marks uncommitted changes.\n"+
			"  GROUP    the group the service belongs to.\n"+
			"  ENV      the active config env this copy is pointed at, or blank.\n"+
			"  RELOAD   auto when the service reloads on its own. manual when you must restart it after an edit.\n"+
			"  ERROR    the last error.\n"+
			"PATH and BRANCH name the code that the process runs. base runs a replica of the workspace, and not the user's checkout. So a base row's PATH is under a .devstack-base directory, and its BRANCH is detached at the default branch tip. The base tool (action=\"path\") prints that directory. Until devstack builds the replica, status shows the checkout in its place.\n"+
			"Status values:\n"+
			"  running   the process is up.\n"+
			"  starting  the copy is on its way up.\n"+
			"  building  the daemon builds or updates it now.\n"+
			"  stopped   devstack knows the service, and the service does not run. Nobody started it, or somebody stopped it.\n"+
			"  erroring  the service failed, or its build failed. Read its logs.\n"+
			"  disabled  somebody switched the resource off in the daemon.\n"+
			"  down      the copy is not registered in the daemon, because its stack is down. Bring the stack up with stack_up.\n"+
			"  unknown   the daemon reported no state for it.\n"+
			"status also shows a summary of the groups. To see a feature stack's copies, pass stack."),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient, serviceDirs: replicaServiceDirs(ws, serviceDirs), cfg: cfg}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		serviceDirs, cfg := t.serviceDirs, t.cfg

		view, err := t.client.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if len(view.UiResources) == 0 {
			return mcp.NewToolResultText(targetHeader(t.label) + "Tilt runs, and no services are loaded yet. Tilt can still be on its way up."), nil
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
		sb.WriteString("\nPATH and BRANCH are the directory this copy runs from. A * marks uncommitted changes.\nA branch that you did not expect means that the process does not hold the work you look for.\nFor base that directory is the replica, and not the checkout devstack built it from. An edit in the checkout reaches a running base copy after three steps: put it on the default branch, run the base tool (action=\"sync\"), then restart that copy (restart tool, stack=\"base\"). In a shell, `devstack workspace up` does all three.\nRELOAD auto means that an edit in the directory a copy runs from applies on its own. RELOAD manual means that you must restart that copy after an edit. If you do not restart it, the copy keeps the old code.\n")

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
			sb.WriteString("\n⚠ observability is enabled for this workspace, and the collector is NOT running. devstack captures no telemetry. To start the collector, run: devstack otel start\n")
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
			return "", fmt.Errorf("service %q is not in workspace %q. Known services: %s", service, graph.WorkspaceName, coreCSV(graph.ServiceNames()))
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

	sb.WriteString("\nThis is declared configuration, not runtime state. To see what runs, use status.\n")
	return sb.String(), nil
}

func registerTopologyTool(mcpServer *server.MCPServer, workspacePath string) {
	tool := mcp.NewTool("topology",
		mcp.WithDescription("Show this workspace's declared service graph. For every service it shows the source directory, the groups, and the call edges recorded for it. It also shows the services it depends on and the services that depend on it. It reports configuration issues too, such as an unknown group member, an unknown dependency, or a dependency cycle. Read this before you claim that one service calls or depends on another. The graph comes from the workspace manifest, and not from a guess at the code. To show one service's node alone, pass service. This is declared configuration, not runtime state. To see what runs, use status."),
		mcp.WithString("service",
			mcp.Description("Exact service name to show alone (for example 'api-service'). NOT a description or partial match. If you omit it, the tool shows the whole graph."),
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
			part = s.name + "=not in the daemon (it can still reload)"
		}
		if !settled && s.found && !s.deployed && coreSettled(s.status) {
			part += " (no new deploy yet)"
		}
		parts = append(parts, part)
	}
	if settled {
		return "after waiting: " + strings.Join(parts, ", ")
	}
	return fmt.Sprintf("waited %ds and these had not settled: %s. A status of 'building' can continue for a long time, because the build is slow or hung. Read the logs.",
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
		mcp.WithDescription("Restart one service copy, or every service copy of a group, in the LOCAL dev stack. devstack triggers a rebuild. This tool acts on local dev services only. The service name must be exact. If you give neither service nor group, the tool uses the default service for this repo (DEVSTACK_DEFAULT_SERVICE sets it)."),
		mcp.WithString("service",
			mcp.Description("Exact service name or configured alias (for example 'api-service'). NOT a description or partial match. If you omit it, and you give no group, the tool uses the default service for this repo."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to restart. devstack restarts every service of the group at the same time. You can not combine this with service."),
		),
		mcp.WithString("stack", mcp.Description(mutatingStackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithNumber("wait_seconds",
			mcp.Description("Wait for this many seconds at most while the restarted services settle. The tool then reports the state each service ended in. The default of 0 returns immediately, before the rebuild is complete. The maximum is 300. At a timeout the tool names the state each service was still in. It does not claim success.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")
		waitSeconds := int(request.GetFloat("wait_seconds", 0))

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
		}

		t, err := resolveMutatingTarget(ws, localTarget{client: tiltClient, cfg: cfg, defaultSvc: defaultService}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient, defaultService := t.client, t.defaultSvc

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
			members, groupNote, err := targetGroupMembers(ws, t, groupName)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
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
				return mcp.NewToolResultError(groupNote + fmt.Sprintf("restarted %d/%d services in group %q: %s\nfailures: %s",
					len(successes), len(members), groupName, strings.Join(successes, ", "), strings.Join(failures, "; "))), nil
			}
			waited := make([]string, 0, len(members))
			for _, svc := range members {
				waited = append(waited, resourceName(ws.Name, svc, t.namespace))
			}
			return mcp.NewToolResultText(syncNotes + groupNote + onTarget(t.label, fmt.Sprintf("restarted %d services in group %s: %s",
				len(members), groupName, strings.Join(successes, ", "))) + coreWaitFor(tiltClient, waited, deployedBefore, waitSeconds)), nil
		}

		// Single service restart.
		if name == "" {
			name = defaultService
		}
		if name == "" {
			return mcp.NewToolResultError("no service specified, and this repo has no default service"), nil
		}

		resolved, err := tilt.ResolveService(resourceName(ws.Name, name, t.namespace), view)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// If the resource is disabled, enable it first
		for _, r := range view.UiResources {
			if r.Metadata.Name == resolved && r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
				if out, err := tiltClient.RunCLI("enable", resolved); err != nil {
					return mcp.NewToolResultError(fmt.Sprintf("devstack can not enable %q: %v\n%s", resolved, err, out)), nil
				}
				break
			}
		}

		out, err := tiltClient.RunCLI("trigger", resolved)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("devstack can not restart %q: %v\n%s", resolved, err, out)), nil
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
		mcp.WithDescription("Start one service copy, or every service copy of a group, in the LOCAL dev stack. Use this tool on a service that status reports as 'stopped' or 'disabled'. Use restart on a service that already runs. devstack reads the dependencies from the workspace's dependency graph. It starts them first, in order, so that a service's callees come up before it. This tool acts on local dev services only. The service name must be exact. If you give neither service nor group, the tool uses the default service for this repo (DEVSTACK_DEFAULT_SERVICE sets it)."),
		mcp.WithString("service",
			mcp.Description("Exact service name or configured alias (for example 'api-service'). NOT a description or partial match. If you omit it, and you give no group, the tool uses the default service for this repo."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to start. devstack starts every service of the group, and it starts the dependencies first. You can not combine this with service."),
		),
		mcp.WithString("stack", mcp.Description(mutatingStackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithNumber("wait_seconds",
			mcp.Description("Wait for this many seconds at most while the started services settle. The tool then reports the state each service ended in. The default of 0 returns immediately, before the startup is complete. The maximum is 300. At a timeout the tool names the state each service was still in. It does not claim success.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")
		waitSeconds := int(request.GetFloat("wait_seconds", 0))
		var waited []string

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
		}

		t, err := resolveMutatingTarget(ws, localTarget{client: tiltClient, cfg: cfg, defaultSvc: defaultService}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient, defaultService, cfg := t.client, t.defaultSvc, t.cfg

		var seeds []string
		var groupNote string
		if groupName != "" {
			members, note, err := targetGroupMembers(ws, t, groupName)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			seeds, groupNote = members, note
		} else {
			if name == "" {
				name = defaultService
			}
			if name == "" {
				return mcp.NewToolResultError("no service specified, and this repo has no default service"), nil
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
			return mcp.NewToolResultError(fmt.Sprintf("nothing to start. The daemon has no resource named %s. Read the name from status.", strings.Join(missing, ", "))), nil
		}

		var sb strings.Builder
		sb.WriteString(syncNotes)
		sb.WriteString(groupNote)
		sb.WriteString(onTarget(t.label, fmt.Sprintf("Started %d service(s) in dependency order: %s.", len(started), strings.Join(started, ", "))))
		if len(missing) > 0 {
			fmt.Fprintf(&sb, "\nthese are not loaded in the daemon, so devstack skipped them: %s", strings.Join(missing, ", "))
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
		mcp.WithDescription("Stop (disable) service copies in the LOCAL dev stack. This tool acts on local dev services only. The service name must be exact.\n"+
			"Give exactly one target:\n"+
			"  service   stops that one service.\n"+
			"  group     stops every service of that group.\n"+
			"  all=true  stops every copy of the target: all of base, or all of one stack.\n"+
			"With none of the three, the tool stops the default service for this repo. That is the same default that restart uses (DEVSTACK_DEFAULT_SERVICE). So a bare call never takes the workspace down. Stopping everything requires all=true.\n"+
			"The stack parameter scopes the call. With stack set, even all=true touches only that stack's copies, never base's."),
		mcp.WithString("service",
			mcp.Description("Exact service name or alias to stop (for example 'api-service'). NOT a description or partial match. If you omit it, the tool stops the default service for this repo, and not every service. To stop every service, pass all=true."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to stop. devstack stops every service of the group at the same time. It stops them in the target only: base, or one stack. You can not combine this with service or all."),
		),
		mcp.WithString("stack", mcp.Description(mutatingStackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithBoolean("all",
			mcp.Description("Stop every service of the target. That is the whole workspace, or the whole stack when stack is set. You must pass all=true to stop more than one service. If you omit service and group, that does NOT mean all. You can not combine all with service or group.")),
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

		t, err := resolveMutatingTarget(ws, localTarget{client: tiltClient, cfg: cfg, defaultSvc: defaultService}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient, defaultService := t.client, t.defaultSvc

		if name == "" && groupName == "" && !stopAll {
			name = defaultService
			if name == "" {
				return mcp.NewToolResultError("no service specified, and this repo has no default service. Pass service, group, or all=true to stop every service"), nil
			}
		}

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Group stop.
		if groupName != "" {
			members, groupNote, err := targetGroupMembers(ws, t, groupName)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
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
				return mcp.NewToolResultError(groupNote + fmt.Sprintf("stopped %d/%d services in group %q: %s\nfailures: %s",
					len(successes), len(members), groupName, strings.Join(successes, ", "), strings.Join(failures, "; "))), nil
			}
			var hookOut strings.Builder
			hookErr := hooks.Fire(ws, t.namespace, config.EventServiceStop, successes, &hookOut)
			var gsb strings.Builder
			gsb.WriteString(groupNote)
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
				return mcp.NewToolResultError(fmt.Sprintf("devstack can not stop %q: %v\n%s", resolved, err, out)), nil
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
			return mcp.NewToolResultError(fmt.Sprintf("These services did not stop:\n%s", sb.String())), nil
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
				fmt.Fprintf(&sb, "\nThese feature stacks still run: %s. A stop across base does not touch them. To stop each one, use stack=<name> with all=true, or use stack_down.", strings.Join(active, ", "))
			}
		}
	}
	sb.WriteString("\nThe host daemon itself is still up. It stops only from the shell (devstack workspace down).")
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
	scope := "every service of this target"
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
		widen = append(widen, "stack=<name> to read a feature stack's copies instead of base's")
	} else {
		widen = append(widen, "omit stack to read base instead of this stack")
	}
	if f.Service != "" || f.Group != "" {
		widen = append(widen, "omit service and group to read every service of this target")
	}
	widen = append(widen, "status to make sure that the service runs")

	return fmt.Sprintf("Empty means that nothing matched these filters — NOT that the service is healthy or silent.\nFilters applied: %s.\nTo widen the search, use one of these:\n  - %s\n",
		strings.Join(applied, ", "), strings.Join(widen, "\n  - "))
}

func registerProcessLogsTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("process_logs",
		mcp.WithDescription("Fetch raw stdout and stderr from a local dev service process that runs now. This is NOT a log search engine. It fetches live process output from the dev daemon.\n"+
			"The parameters are structured: an exact service name, an integer line count, and boolean flags. The tool does NOT accept a natural language query. Example: service='api-service' lines=100 since_restart=true.\n"+
			"Use it for a service with no OTEL instrumentation, or when you need unstructured process output.\n"+
			"If you give no service, the tool uses this repo's default service where one is configured. Where there is none, it fetches every service at the same time.\n"+
			"It supports a grep filter, paging with offset, and since_restart to isolate the output after startup. With a group, it fetches the logs of every service of the group at the same time. You can not give both service and group."),
		mcp.WithString("service",
			mcp.Description("Exact service name or alias (for example 'api-service'). NOT a description or partial match. If you omit it, the tool uses the default service for this repo, or it fetches every service."),
		),
		mcp.WithString("group",
			mcp.Description("Group name. The tool fetches the logs of every service of the group at the same time. Each line carries the service name as a prefix. You can not combine this with service."),
		),
		mcp.WithNumber("lines",
			mcp.Description("The number of lines to return, as an integer. The default is 100."),
		),
		mcp.WithNumber("offset",
			mcp.Description("Skip this many lines from the most recent end, then return `lines` lines. Use it to page backward: offset=0 gives the last 100 lines, and offset=100 gives the 100 lines before those. The default is 0."),
		),
		mcp.WithString("grep",
			mcp.Description("Regex pattern that filters the lines. The tool returns only the lines that match this pattern. To include the lines around each match, use context."),
		),
		mcp.WithNumber("context",
			mcp.Description("The number of lines to include before and after each grep match (the same as grep -C N). The tool uses it only when grep is set. The default is 0."),
		),
		mcp.WithBoolean("since_restart",
			mcp.Description("If true, the tool returns only the lines since the last deploy or restart of the service. It uses the deploy timestamp of the dev daemon, and no heuristic. The default is true."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, the tool returns only the lines that match error, exception, panic, fatal or fail. The default is false."),
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
				return fmt.Sprintf("(offset %d is more than the %d lines available)", offset, total)
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
					fmt.Fprintf(&sb, "[%s] devstack can not read the logs: %v\n", r.svc, r.err)
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
				return mcp.NewToolResultError(fmt.Sprintf("devstack can not read the logs of %q: %v\n%s", resolved, err, raw)), nil
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
				fmt.Fprintf(&sb, "devstack can not read the logs: %v\n\n", r.err)
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
		mcp.WithDescription("Read or set a runtime argument of the dev daemon.\n"+
			"A Tiltfile sees an argument only when it calls config.parse or config.define_*. The Tiltfile that devstack generates calls neither. So on a daemon that devstack manages, this tool refuses a write. Such a write restarts every service and changes nothing.\n"+
			"Where a Tiltfile does read arguments, call this tool with no key first, and read the arguments that are already set. A write REPLACES the whole list, and it silently drops every argument that you do not pass again. This tool sets one key, so every argument set earlier is dropped.\n"+
			"Use this tool to change a feature flag, a mode, or another runtime setting. devstack restarts the services that the change affects.\n"+
			"Boundary: this tool sets the arguments that the daemon itself reads when it generates the stack. For a config value that a service reads, use env_use (it points a scope at a named config env), env_set (it edits a named env's vars) or service_env (it edits one service's vars).\n"+
			"An argument belongs to the one host daemon. It does not belong to base or to a stack. So the stack parameter scopes nothing here, unlike on every other tool. It only requires that the named stack is up. A write with stack set changes the same single list as a write without it."),
		mcp.WithString("key",
			mcp.Description("The argument key (for example 'env', 'debug', 'profile'). To read the arguments that are set, omit both key and value."),
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
				return mcp.NewToolResultError("you gave a value with no key. To set an argument, pass both. To read the arguments that are set, pass neither"), nil
			}
			return mcp.NewToolResultText(coreCurrentArgs(tiltClient)), nil
		}
		if value == "" {
			return mcp.NewToolResultError(fmt.Sprintf("no value for %q. To set it, pass value. To read the current arguments, omit key", key)), nil
		}
		if !coreTiltfileReadsArgs() {
			return mcp.NewToolResultError("this daemon's Tiltfile declares no arguments. A write restarts the services and changes nothing. devstack generates that Tiltfile, and it never calls config.define_string or config.parse. A Tiltfile needs one of those two calls to read an argument. To configure a service, use its env instead: env_use, env_set, or service_env."), nil
		}

		out, err := tiltClient.RunCLI("args", "--", fmt.Sprintf("%s=%s", key, value))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("devstack can not set %s=%s: %v\n%s", key, value, err, out)), nil
		}

		return mcp.NewToolResultText(onTarget(t.label, fmt.Sprintf("Set %s=%s. devstack restarts the services that this change affects.", key, value))), nil
	})
}

// coreOtelUIService resolves the observability UI as a forwardable port, the
// same resolution `devstack tunnel --otel` uses. It is not a daemon resource, so
// it is never discovered — it is only added on request.
func coreOtelUIService(ws *workspace.Workspace) (svc tunnel.Service, reason string, ok bool) {
	plugin := otel.For(ws)
	if plugin == nil {
		return tunnel.Service{}, "no observability backend is configured", false
	}
	endpoint := plugin.QueryEndpoint(ws)
	if endpoint == "" {
		return tunnel.Service{}, fmt.Sprintf("backend %q has no local UI to forward. The telemetry goes upstream instead", plugin.Name()), false
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
		mcp.WithDescription("Forward this workspace's LOCAL service ports to a remote host over SSH, or from one. A remote machine can then reach the services that run on this dev box.\n\n"+
			"Actions:\n"+
			"  list    the ports a push forwards, with the exact service name of each and whether it serves now. It changes nothing.\n"+
			"  status  what this workspace forwards now. Add planned=true to see what a push will forward instead. It reads this machine only.\n"+
			"  check   ask the remote host what already holds the ports a push binds. It changes nothing. Run it before reclaim=true, which kills those processes.\n"+
			"  push    expose local ports on the remote over ssh -R. This is the common case.\n"+
			"  pull    bring ports from a source machine to here over ssh -L.\n"+
			"  stop    tear down the forwards of this workspace. Narrow it with service.\n\n"+
			"This tool needs key-based SSH access to the remote. Passwords do not work. If the keys are absent, the result tells you to run ssh-copy-id.\n"+
			"It forwards only the ports that serve traffic now, and it skips a service that is idle or dead.\n"+
			"Any host that you can reach over ssh works, and a plain ssh-config alias is one. A tailnet address is one such host, and not a requirement.\n\n"+
			"status and stop read the forwards that run, and not what discovery covers now. So they report and tear down the observability UI and the forwards of a stack, whether or not this call asked for them.\n"+
			"After the first push or pull, devstack remembers the remote, the direction and the stack mapping. A later `devstack tunnel restart` in a shell then repeats the same thing.\n"),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("One of: list, status, check, push, pull, stop. These read and change nothing: 'list', 'status', 'check'. (check does open an ssh session to read the far host's ports, and it alters nothing there.) These write: 'push', 'pull', 'stop'. They start or kill ssh forwards, and push and pull also save the remote.")),
		mcp.WithString("host",
			mcp.Description("Remote host or SSH config alias (for example 'macbook'). It is optional where a default is saved for this workspace.")),
		mcp.WithString("user",
			mcp.Description("SSH user. It is optional, and it falls back to the saved user for this workspace.")),
		mcp.WithString("service",
			mcp.Description("Exact service names to limit the call to, comma-separated, as action=list prints them (CLI: --service). It is optional. The default is every service that serves traffic. For 'stop' the default is every forward that this workspace runs.")),
		mcp.WithBoolean("reclaim",
			mcp.Description("Push only. Before devstack forwards, it kills whatever already holds these ports on the remote. CAUTION: this is destructive. It tears down forwards that belong to other stacks. Leave it off until a push fails to bind and you know that the port is yours.")),
		mcp.WithBoolean("stacks",
			mcp.Description("Also forward every active feature stack. Each one uses its OWN allocated port. The far end reaches them at those ports, and not at the usual ones. The default is false. You can not combine this with as_base.")),
		mcp.WithString("as_base",
			mcp.Description("Put ONE feature stack on base's ports. Name the stack. The far end then reaches that stack's copies at the addresses base usually serves, and nobody has to reconfigure anything over there. This is the answer to \"let them test my stack on the usual URLs\". You can not combine this with stacks.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithBoolean("otel",
			mcp.Description("Also forward the port of the observability UI, so that the remote can read this machine's telemetry at the address you use here (CLI: --otel). The default is false. Where the backend has no local UI, the tool ignores this and prints a note.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace for tunnels"), nil
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
			sb.WriteString("These are the ports a push forwards. This call starts nothing:\n")
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
				return mcp.NewToolResultError("no remote host or user given, and none is saved. Pass host and user. devstack remembers them after the first push that succeeds."), nil
			}
			if len(svcs) == 0 {
				return mcp.NewToolResultText(otelNote + "No ports to check now. Nothing serves traffic. Start the services first."), nil
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
				fmt.Fprintf(&sb, "\n%d port(s) are held. A push needs reclaim=true, and that kills those processes. They can belong to a colleague, or to another stack. Narrow the call with service.\n", held)
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
				return mcp.NewToolResultError("no remote host given, and none is saved. Pass host. devstack remembers it after the first push that succeeds."), nil
			}
			ruser := user
			if ruser == "" {
				ruser = ws.TunnelUser
			}
			if ruser == "" {
				return mcp.NewToolResultError("no SSH user given, and none is saved. Pass user. devstack remembers it after the first push that succeeds."), nil
			}

			var skipped []tunnel.Service
			if mode == tunnel.ModePush {
				svcs, skipped = tunnel.PartitionServing(svcs)
			}
			if len(svcs) == 0 {
				return mcp.NewToolResultText("No port serves traffic now, so there is nothing to forward. Start the services first with the start tool, and pass stack=\"base\" or a stack name."), nil
			}

			if cerr := tunnel.CheckConnectivity(ruser, rhost); cerr != nil {
				return mcp.NewToolResultText(fmt.Sprintf(
					"devstack can not open an SSH session to %s@%s.\nssh: %s\n\ndevstack tunnels use key-based SSH, and no passwords. To enable it:\n  1. ssh %s@%s\n  2. ssh-copy-id %s@%s\n  3. call this tool again.",
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
				fmt.Fprintf(&sb, "\nA forward fails when something already holds the port on %s. That can be a stale "+
					"forward of your own, or it can belong to another stack. Use action=check before you call push again with reclaim=true.\n", rhost)
			}
			return mcp.NewToolResultText(sb.String()), nil

		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use list, status, check, push, pull, or stop", action)), nil
		}
	})
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
		stackScope = "base and every feature stack"
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
		widen = append(widen, fmt.Sprintf("name a different service, or search by attribute and value to span services rather than pin %s", f.Service))
	}
	widen = append(widen, fmt.Sprintf("raise limit above %d", f.Limit))
	widen = append(widen, "the observability tool's status action, to see whether these services emit telemetry at all")

	var sb strings.Builder
	sb.WriteString("Empty means that nothing matched these filters — NOT that the service is healthy, idle or uninstrumented.\n")
	fmt.Fprintf(&sb, "Filters applied: %s.\n", strings.Join(applied, ", "))
	if f.ErrorsOnly && f.MatchedBeforeErrorsOnly > 0 {
		fmt.Fprintf(&sb, "%d execution(s) matched everything except errors_only. Traffic exists, and none of it errored.\n", f.MatchedBeforeErrorsOnly)
	}
	fmt.Fprintf(&sb, "To widen the search, use one of these:\n  - %s\n", strings.Join(widen, "\n  - "))
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
		"Investigate distributed traces in the LOCAL dev environment. The server resolved the backend at start: %s. The observability tool reports the current backend, and it can change it.\n"+
			"This tool queries this workspace's configured telemetry backend. It is NOT a natural language search engine. The parameters are structured: exact service names, structured time ranges, and exact attribute key and value pairs.\n"+
			"Results always stay inside this workspace's telemetry. In mode 3 a call with no service narrows to the service that the server runs in.\n"+
			"Modes, and what each one filters on:\n"+
			"  (1) trace_id or span_id — look up one trace or span by id. Here every other filter is ignored, including service, stack, since_minutes, limit and errors_only. So the trace comes back whichever copy emitted it, and however old it is.\n"+
			"  (2) attribute and value — search by business attribute, for example attribute='portfolio.id' value='123'. This mode filters by stack, since_minutes, limit and errors_only. It also filters by service when you give service. This mode has no default service.\n"+
			"  (3) service — recent executions. This mode filters by stack, since_minutes, limit, errors_only and service. Where you omit service, the tool falls back to this repo's default service.\n"+
			"A query against a stack that is down is not an error, and it is rarely useful. It returns only what that stack emitted while it was last up. So an empty result there means that the stack is down, and not that it is healthy. stack_list reports which stacks are up.\n"+
			"One backend holds every workspace and every stack, so resource attributes tell the results apart:\n"+
			"  devstack.workspace  devstack applies this one for you, always.\n"+
			"  devstack.service    the name devstack uses. It often differs from the name the service reports for itself, and either name matches.\n"+
			"  devstack.stack      base, or a feature stack's name.\n"+
			"  devstack.env        the config env that copy runs under.\n"+
			"In modes 2 and 3 you can isolate the results to one stack's service. 'service' pins the service, and 'stack' pins the devstack.stack resource attribute. "+observability.StackScopeDesc+"\n"+
			"To compare a feature stack against base, run the same query twice: once with stack='<name>', and once with stack='base'. Before you conclude that a service is silent, use the observability tool's status action to see which copies emit telemetry.\n"+
			"Example: service='api-service' stack='perf' since_minutes=15 errors_only=true.\n"+
			"The tool returns an ASCII span tree that shows service calls, durations and errors. Combine it with process_logs and status for the full debugging context.",
		queryURL,
	)

	tool := mcp.NewTool("investigate",
		mcp.WithDescription(desc),
		mcp.WithString("trace_id",
			mcp.Description("Specific trace ID to look up. If given, every other filter is ignored — including service, stack, since_minutes, limit and errors_only."),
		),
		mcp.WithString("span_id",
			mcp.Description("Specific span ID to look up. The tool finds the trace that holds this span, and it ignores every other filter. If you give trace_id, the tool ignores span_id."),
		),
		mcp.WithString("service",
			mcp.Description("Exact service name (for example 'api-service'). NOT a description or partial match. The tool applies it in mode 2 (attribute search) and in mode 3 (recent executions). Only mode 3 falls back to this repo's default service when you omit it. A trace_id/span_id lookup ignores it, and it returns every service's spans in that trace."),
		),
		mcp.WithString("stack",
			mcp.Description("Whose telemetry to query, through the devstack.stack resource attribute. "+observability.StackScopeDesc+" The tool applies this parameter in mode 2 (attribute search) and in mode 3 (recent executions) ONLY. A trace_id/span_id lookup is not stack-filtered, and it returns the trace whichever copy emitted it. 'devstack otel traces --stack' has the same three readings, so the CLI and this tool cover the same copies. Combine it with 'service' to pin one copy's service."),
		),
		mcp.WithString("attribute",
			mcp.Description("Exact attribute key to search by (for example 'portfolio.id', 'user.id', 'process.id'). NOT natural language. Requires value parameter."),
		),
		mcp.WithString("value",
			mcp.Description("Exact value to match for the given attribute (for example '123'). NOT a pattern or description."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Look-back window in minutes, as an integer. The default is 30. To search further back, use a larger value, for example 60."),
		),
		mcp.WithNumber("limit",
			mcp.Description("The maximum number of executions to expand. The default is 5."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, the tool returns only the executions whose root span has an error status. The default is false."),
		),
		mcp.WithBoolean("verbose",
			mcp.Description("If true, the tool shows every span attribute and the full correlated logs. The default of false returns a compact view."),
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
				msg := fmt.Sprintf("The active OTEL plugin (%s) has no local query UI. devstack forwards the telemetry to %s. Query it there instead.", localPluginName, localPluginUpstream)
				return mcp.NewToolResultText(msg), nil
			}
			return mcp.NewToolResultError("The observability backend is not configured. Read your environment settings."), nil
		}

		traceID := request.GetString("trace_id", "")
		spanID := request.GetString("span_id", "")
		service := request.GetString("service", "")
		stack := observability.ResolveStackFilter(request.GetString("stack", ""))
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
		return "No arguments are set on the daemon. A write replaces the whole list. With none set, there is nothing to keep.\n"
	}
	return fmt.Sprintf("Arguments set on the daemon: %s\nA write replaces this whole list. Pass every argument that you want to keep.\n", strings.Join(args, " "))
}
