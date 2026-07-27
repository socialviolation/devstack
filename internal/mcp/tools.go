package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
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

// RegisterTools registers devstack MCP tools: status, restart, stop, configure,
// process_logs, service_env, observability, stack and env tools, plus investigate
// when observability is enabled and tunnel when tailscale is installed.
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
	// which of the capability-gated tools below actually exist for this context.
	registerEnvironmentTool(mcpServer, obsURL, workspaceName, workspacePath, ws)

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
	registerRestartTool(mcpServer, tiltClient, defaultService, cfg, ws)
	registerStopTool(mcpServer, tiltClient, cfg, ws)
	registerConfigureTool(mcpServer, tiltClient, ws)
	registerProcessLogsTool(mcpServer, tiltClient, defaultService, cfg, ws)
	registerServiceEnvTool(mcpServer, ws, workspacePath)

	// Observability control (status/enable/disable/configure) is always
	// available so an agent can discover and turn it on.
	registerObservabilityTool(mcpServer, ws, workspacePath)

	// The trace-query tool only makes sense when the workspace has opted into
	// observability — otherwise there's no collector or backend to query.
	// (Telemetry evidence/confidence lives in the observability tool's status.)
	if config.ObservabilityEnabled(workspacePath) {
		registerInvestigateTool(mcpServer, tiltClient, defaultService, backend, obsURL, workspacePath, ws)
	}

	// Tunneling is SSH-over-tailnet; only expose it where tailscale exists.
	if tailscaleInstalled() {
		registerTunnelTool(mcpServer, tiltClient, ws)
	}

	// Feature stacks overlay this workspace as their base.
	registerStackTools(mcpServer, ws)

	// Config-patch environments: point scopes at named envs and inspect them.
	registerEnvTools(mcpServer, ws, workspacePath)
}

// tailscaleInstalled reports whether the tailscale CLI is on PATH. devstack
// tunnels reach remote hosts over a tailnet, so the tunnel tool is only exposed
// when tailscale is present on this machine.
func tailscaleInstalled() bool {
	_, err := exec.LookPath("tailscale")
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
		mcp.WithDescription("Show the current status of all services in the LOCAL dev stack. Status reflects the current state of locally running dev services, not production. Returns SERVICE, STATUS (one of running/starting/building/stopped/erroring/disabled/unknown), PORT(S), PATH (source directory), BRANCH (the git branch that directory is on, with * for uncommitted changes — this is the code the process is actually running), GROUP, ENV (the active environment/config-patch the instance is pointed at, blank if none), and last error. Also shows a groups summary. 'running' means the process is up. 'starting' means it is coming up; 'building' means the daemon is building/updating it. 'stopped' means the service is known but not currently running (not started yet, or was stopped). 'erroring' means the service or its build failed — check logs. 'disabled' means the resource is switched off in the daemon. 'unknown' means the daemon reported no state for it. Pass stack to see a feature stack's instances."),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
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

		rows := [][]string{{"SERVICE", "STATUS", "PORT(S)", "PATH", "BRANCH", "GROUP", "ENV", "ERROR"}}
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
			if rw != nil {
				if rs, ok := rw.Services[svc]; ok && rs.Manifest != nil {
					svcEnv = rs.Manifest.Service.Env
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
			rows = append(rows, []string{name, status, ports, path, branch, group, env, lastError})
		}

		var sb strings.Builder
		sb.WriteString(targetHeader(t.label))
		sb.WriteString("Tilt is running.\n\n")
		sb.WriteString(renderColumns(rows))
		sb.WriteString("\nBRANCH is the git checkout each service actually runs from; * marks uncommitted changes.\nA service runs the code on that branch — if it is not the branch you expect, the running process does not contain the work you are looking for.\n")

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

func registerRestartTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("restart",
		mcp.WithDescription("Restart a specific service or all services in a group in the LOCAL dev stack by triggering a rebuild. Operates on local dev services only — service name must be exact. If neither service nor group is given, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("service",
			mcp.Description("Exact service name or configured alias (e.g. 'api-service'). NOT a description or partial match. If omitted, uses the default service for this repo (unless group is given)."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to restart. All services in the group are restarted in parallel. Cannot be combined with service."),
		),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
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

		// Regenerate first so an edit to a manifest is what gets restarted.
		syncNotes := strings.Join(hostdaemon.SyncAndReload(tiltClient), "\n")
		if syncNotes != "" {
			syncNotes += "\n"
		}

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

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
			return mcp.NewToolResultText(syncNotes + onTarget(t.label, fmt.Sprintf("restarted %d services in group %s: %s",
				len(members), groupName, strings.Join(successes, ", ")))), nil
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

func registerStopTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("stop",
		mcp.WithDescription("Stop (disable) one service, all services in a group, or all services in the LOCAL dev stack. Operates on local dev services only — service name must be exact. If service is given, stops that service. If group is given, stops all services in the group. If neither is given, stops all services. Cannot specify both service and group."),
		mcp.WithString("service",
			mcp.Description("Exact service name or alias to stop (e.g. 'api-service'). NOT a description or partial match. If omitted, all services are stopped (unless group is given)."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to stop. All services in the group are stopped in parallel. Cannot be combined with service."),
		),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
		}

		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient, cfg: cfg}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient, cfg := t.client, t.cfg

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
			return mcp.NewToolResultText(onTarget(t.label, fmt.Sprintf("stopped %d services in group %s: %s",
				len(members), groupName, strings.Join(successes, ", ")))), nil
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
			return mcp.NewToolResultText(onTarget(t.label, fmt.Sprintf("Stopped %q.", resolved))), nil
		}

		// Stop all (scoped to the stack's resources when a stack is targeted).
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
		return mcp.NewToolResultText(onTarget(t.label, fmt.Sprintf("Stopped %d service(s).", len(targets))) + "\n" + sb.String()), nil
	})
}

func registerProcessLogsTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig, ws *workspace.Workspace) {
	tool := mcp.NewTool("process_logs",
		mcp.WithDescription("Fetch raw stdout/stderr from a locally running dev service process. NOT a log search engine — fetches live process output directly from the dev daemon. Parameters are structured: exact service name, integer line count, boolean flags. Natural language queries are NOT accepted. Example: service='api-service' lines=100 since_restart=true. Use for services not instrumented with OTEL or when you need unstructured process output. If no service is given, uses the default or fetches all services in parallel. Supports grep filtering, paging via offset, and since_restart to isolate post-startup output. When group is given, fetches logs from all services in the group concurrently. Cannot specify both service and group."),
		mcp.WithString("service",
			mcp.Description("Exact service name or alias (e.g. 'api-service'). NOT a description or partial match. If omitted, uses the default service for this repo or fetches all."),
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
			for _, r := range results {
				if r.err != nil {
					fmt.Fprintf(&sb, "[%s] error fetching logs: %v\n", r.svc, r.err)
					continue
				}
				if r.out == "" {
					fmt.Fprintf(&sb, "[%s] (no output)\n", r.svc)
					continue
				}
				prefix := fmt.Sprintf("[%s] ", r.svc)
				for _, line := range strings.Split(r.out, "\n") {
					fmt.Fprintf(&sb, "%s%s\n", prefix, line)
				}
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
				return mcp.NewToolResultText(targetHeader(t.label) + fmt.Sprintf("No matching output in %s.", resolved)), nil
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
		for _, r := range results {
			fmt.Fprintf(&sb, "=== %s ===\n", r.name)
			if r.err != nil {
				fmt.Fprintf(&sb, "error fetching logs: %v\n\n", r.err)
			} else if r.out == "" {
				sb.WriteString("(no output)\n\n")
			} else {
				sb.WriteString(r.out)
				sb.WriteString("\n\n")
			}
		}
		return mcp.NewToolResultText(targetHeader(t.label) + sb.String()), nil
	})
}

// lastDeploySince returns a --since duration string (e.g. "127s") for the given service,
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
		// Add 2s buffer so we don't miss the first lines right at deploy time.
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
		mcp.WithDescription("Set a dev daemon runtime argument (key=value) that controls how services are configured. Use this to change feature flags, modes, or other runtime config. Affected services will restart automatically."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The argument key (e.g. 'env', 'debug', 'profile')."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The value to set (e.g. 'production', 'true', 'staging')."),
		),
		mcp.WithString("stack", mcp.Description(stackParamDesc)),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := request.GetString("key", "")
		value := request.GetString("value", "")
		if key == "" {
			return mcp.NewToolResultError("key must not be empty"), nil
		}

		t, err := resolveLocalTarget(ws, localTarget{client: tiltClient}, request.GetString("stack", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		tiltClient := t.client

		out, err := tiltClient.RunCLI("args", "--", fmt.Sprintf("%s=%s", key, value))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set %s=%s: %v\n%s", key, value, err, out)), nil
		}

		return mcp.NewToolResultText(onTarget(t.label, fmt.Sprintf("Set %s=%s. Affected services will restart automatically.", key, value))), nil
	})
}

func registerTunnelTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, ws *workspace.Workspace) {
	tool := mcp.NewTool("tunnel",
		mcp.WithDescription("Forward this workspace's LOCAL service ports to/from a remote host over SSH, so a remote machine can reach services running on this dev box. "+
			"Requires key-based SSH access to the remote (no passwords) — if keys aren't installed you'll get instructions to run ssh-copy-id. "+
			"Only ports that are actually serving traffic are forwarded; dead/idle services are skipped. "+
			"The remote host/user are remembered per-workspace after the first successful push, so later calls can omit them. "+
			"Actions: 'list' (discovered services + whether each is serving), 'status' (which tunnels are currently up), "+
			"'push' (expose local ports on the remote via ssh -R — the common case), 'pull' (pull ports from a source machine to here via ssh -L), "+
			"'stop' (tear down all tunnels). The remote is saved automatically on the first successful push/pull."),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("One of: list, status, push, pull, stop.")),
		mcp.WithString("host",
			mcp.Description("Remote host or SSH config alias (e.g. 'macbook'). Optional if a default is saved for this workspace.")),
		mcp.WithString("user",
			mcp.Description("SSH user. Optional — falls back to the saved user for this workspace.")),
		mcp.WithString("services",
			mcp.Description("Comma-separated exact service names to limit to. Optional; default is all serving services.")),
		mcp.WithBoolean("reclaim",
			mcp.Description("Push only. Kill whatever already holds these ports on the remote before forwarding. Destructive: it tears down forwards belonging to other stacks, so leave it off unless a push failed to bind and you know the port is yours.")),
		mcp.WithBoolean("stacks",
			mcp.Description("Also forward this workspace's active feature-stack service ports. Default false — only the workspace's base services are forwarded.")),
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
		for _, s := range strings.Split(request.GetString("services", ""), ",") {
			if s = strings.TrimSpace(s); s != "" {
				filter[s] = true
			}
		}
		svcs := tunnel.Discover(view, filter, ws.Name, request.GetBool("stacks", false))
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Port < svcs[j].Port })

		switch action {
		case "list":
			var sb strings.Builder
			sb.WriteString("Discovered services:\n")
			for _, s := range svcs {
				state := "not serving"
				if tunnel.Listening(s.Port) {
					state = "serving"
				}
				fmt.Fprintf(&sb, "  %-30s :%d  (%s)\n", s.Name, s.Port, state)
			}
			return mcp.NewToolResultText(sb.String()), nil

		case "status":
			var sb strings.Builder
			if ws.TunnelHost != "" {
				fmt.Fprintf(&sb, "remote: %s@%s\n", ws.TunnelUser, ws.TunnelHost)
			}
			for _, s := range svcs {
				state := "down"
				if tunnel.IsUp(ws.Name, s.Port) {
					state = "up"
				}
				fmt.Fprintf(&sb, "  [%-4s] %-30s :%d\n", state, s.Name, s.Port)
			}
			return mcp.NewToolResultText(sb.String()), nil

		case "stop":
			for _, s := range svcs {
				tunnel.KillPort(ws.Name, s.Port)
			}
			return mcp.NewToolResultText(fmt.Sprintf("Stopped tunnels for %d service(s).", len(svcs))), nil

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
				return mcp.NewToolResultError("no remote host given and none saved. Pass host (it's remembered after the first successful push)."), nil
			}
			ruser := user
			if ruser == "" {
				ruser = ws.TunnelUser
			}
			if ruser == "" {
				return mcp.NewToolResultError("no SSH user given and none saved. Pass user (it's remembered after the first successful push)."), nil
			}

			var skipped []tunnel.Service
			if mode == tunnel.ModePush {
				svcs, skipped = tunnel.PartitionServing(svcs)
			}
			if len(svcs) == 0 {
				return mcp.NewToolResultText("No serving ports to forward right now. Start the services first (devstack start)."), nil
			}

			if cerr := tunnel.CheckConnectivity(ruser, rhost); cerr != nil {
				return mcp.NewToolResultText(fmt.Sprintf(
					"Can't open an SSH session to %s@%s.\nssh: %s\n\ndevstack tunnels use key-based SSH (no passwords). Enable it with:\n  1. ssh %s@%s\n  2. ssh-copy-id %s@%s\n  3. retry this tool.",
					ruser, rhost, cerr, ruser, rhost, ruser, rhost)), nil
			}

			// Remember an explicitly provided remote now that it's known to work.
			if host != "" || user != "" {
				_ = workspace.UpdateTunnelRemote(ws.Name, rhost, ruser)
			}

			reclaim := request.GetBool("reclaim", false)
			if mode == tunnel.ModePush && reclaim {
				ports := make([]int, len(svcs))
				for i, s := range svcs {
					ports[i] = s.Port
				}
				tunnel.ReclaimRemote(ruser, rhost, ports)
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "%s tunnels → %s@%s\n", strings.ToUpper(action), ruser, rhost)
			for _, s := range skipped {
				fmt.Fprintf(&sb, "  [skip]    %-30s :%d  (not serving)\n", s.Name, s.Port)
			}
			var clashed bool
			for _, s := range svcs {
				pid, lerr := tunnel.Launch(ws.Name, mode, ruser, rhost, s.Port)
				if lerr != nil {
					clashed = true
					fmt.Fprintf(&sb, "  [FAILED]  %-30s :%d  (%v)\n", s.Name, s.Port, lerr)
					continue
				}
				fmt.Fprintf(&sb, "  [started] %-30s :%d  (pid %d)\n", s.Name, s.Port, pid)
			}
			if clashed && mode == tunnel.ModePush && !reclaim {
				fmt.Fprintf(&sb, "\nA forward fails when something already holds the port on %s. It may be a stale "+
					"forward of your own, or it may belong to another stack — check before retrying with reclaim=true.\n", rhost)
			}
			return mcp.NewToolResultText(sb.String()), nil

		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use list, status, push, pull, or stop", action)), nil
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
		"Investigate distributed traces in the LOCAL dev environment (@ %s). "+
			"Queries this workspace's configured telemetry backend — NOT a natural language search engine. Parameters are structured: exact service names, structured time ranges, and exact attribute key=value pairs. "+
			"Results are always confined to this workspace's telemetry, and an unqualified call narrows to the service the server is running in. "+
			"Modes: (1) trace_id/span_id — look up a specific trace or span; (2) attribute+value — search by business attribute (e.g. attribute='portfolio.id' value='123'); (3) service — show recent executions for a service. "+
			"One backend holds every workspace and every stack, so results are told apart by resource attributes: devstack.workspace (applied for you, always), devstack.service (the name devstack uses, which often differs from the name the service reports itself as — either matches), devstack.stack (base, or a feature stack's name), devstack.env (the config env that instance runs under). "+
			"Results can be isolated to one stack's service: 'service' pins the service and 'stack' pins the devstack.stack resource attribute (a stack's short name, or 'stack'='base' to select base-workspace services). "+
			"To compare a feature stack against base, run the same query twice with stack='<name>' and stack='base'. Use the observability tool's status action to see which variants are actually emitting before concluding a service is silent. "+
			"Example: service='api-service' stack='perf' since_minutes=15 errors_only=true. "+
			"Returns an ASCII span tree showing service calls, durations, and errors. Combine with process_logs and status for full debugging context.",
		queryURL,
	)

	tool := mcp.NewTool("investigate",
		mcp.WithDescription(desc),
		mcp.WithString("trace_id",
			mcp.Description("Specific trace ID to look up. If given, all other filters are ignored."),
		),
		mcp.WithString("span_id",
			mcp.Description("Specific span ID to look up. Finds the trace containing this span. Ignored if trace_id is given."),
		),
		mcp.WithString("service",
			mcp.Description("Exact service name (e.g. 'api-service'). NOT a description or partial match. Only applied in mode 3 (no trace_id or attribute given); attribute searches and trace lookups span all services."),
		),
		mcp.WithString("stack",
			mcp.Description("Which instance's telemetry to query, via the devstack.stack resource attribute. ABSENT/empty = base only (the base-workspace services — the default an unqualified query means). A stack's short name (e.g. 'perf') = that stack only. 'all' (or '*') = every instance co-mingled (base + all stacks). Combine with 'service' to pin a single instance's service. Applied in mode 2 (attribute search) and mode 3 (recent executions)."),
		),
		mcp.WithString("attribute",
			mcp.Description("Exact attribute key to search by (e.g. 'portfolio.id', 'user.id', 'process.id'). NOT natural language. Requires value parameter."),
		),
		mcp.WithString("value",
			mcp.Description("Exact value to match for the given attribute (e.g. '123'). NOT a pattern or description."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Look-back window in minutes (integer). Defaults to 5. Use larger values (e.g. 60) to search further back."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of executions to expand. Defaults to 3."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, only return executions where the root span has error status. Defaults to false."),
		),
		mcp.WithBoolean("verbose",
			mcp.Description("If true, show all span attributes and full correlated logs. Default false returns compact view."),
		),
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
		sinceMinutes := int(request.GetFloat("since_minutes", 5))
		limit := int(request.GetFloat("limit", 3))
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
			out := formatExecutionDetailView(&d, opts)
			out += buildServiceMapSection(ctx, backend, workspacePath, traces[0])
			return mcp.NewToolResultText(out), nil
		}

		// Mode 2: attribute search
		var traceGroups [][]observability.Span
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
			}
			if service == "" {
				service = serviceFromCwd()
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
			return mcp.NewToolResultText(msg), nil
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

		// Async: persist service map edges from all observed spans
		var allObsSpans []observability.Span
		for _, tg := range traceGroups {
			allObsSpans = append(allObsSpans, tg...)
		}
		if len(allObsSpans) > 0 {
			go persistServiceMapEdges(allObsSpans, workspacePath)
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

// buildServiceMapSection returns a service map summary string (after async edge persistence).
// This is a no-op placeholder; actual persistence happens async via persistServiceMapEdges.
func buildServiceMapSection(ctx context.Context, backend observability.Backend, workspacePath string, spans []observability.Span) string {
	return ""
}

// persistServiceMapEdges extracts service edges from spans and merges into .devstack.json.
// Called async from the investigate tool handler.
func persistServiceMapEdges(spans []observability.Span, workspacePath string) {
	if workspacePath == "" || len(spans) == 0 {
		return
	}
	newEdges := extractEdgesFromSpans(spans)
	if len(newEdges) == 0 {
		return
	}
	cfg, err := loadWorkspaceConfig(workspacePath)
	if err != nil {
		return
	}
	existing := cfg.ServiceMapEdges
	merged := mergeServiceEdges(existing, newEdges)
	cfg.ServiceMapEdges = merged
	cfg.ServiceMapUpdatedAt = time.Now()
	saveWorkspaceConfig(workspacePath, cfg)
}

// serviceMapEdge is a simple directed edge between two services.
type serviceMapEdge struct {
	From string
	To   string
}

// extractEdgesFromSpans derives service call edges from a set of spans.
func extractEdgesFromSpans(spans []observability.Span) []serviceMapEdge {
	spanService := map[string]string{}
	for _, s := range spans {
		spanService[s.SpanID] = s.Service
	}
	seen := map[string]bool{}
	var edges []serviceMapEdge
	for _, s := range spans {
		if s.ParentSpanID == "" {
			continue
		}
		parentService, ok := spanService[s.ParentSpanID]
		if !ok || parentService == s.Service {
			continue
		}
		key := parentService + "→" + s.Service
		if !seen[key] {
			seen[key] = true
			edges = append(edges, serviceMapEdge{From: parentService, To: s.Service})
		}
	}
	return edges
}

// mergeServiceEdges deduplicates edges.
func mergeServiceEdges(existing, newEdges []serviceMapEdge) []serviceMapEdge {
	seen := map[string]bool{}
	result := make([]serviceMapEdge, 0, len(existing)+len(newEdges))
	for _, e := range existing {
		key := e.From + "→" + e.To
		if !seen[key] {
			seen[key] = true
			result = append(result, e)
		}
	}
	for _, e := range newEdges {
		key := e.From + "→" + e.To
		if !seen[key] {
			seen[key] = true
			result = append(result, e)
		}
	}
	return result
}

// mutableWorkspaceConfig is a minimal struct for reading/writing the service map portion of .devstack.json.
type mutableWorkspaceConfig struct {
	raw                 map[string]interface{}
	ServiceMapEdges     []serviceMapEdge
	ServiceMapUpdatedAt time.Time
}

func loadWorkspaceConfig(workspacePath string) (*mutableWorkspaceConfig, error) {
	// This is a lightweight bridge; we use the config package for actual loading
	// to avoid an import cycle risk. We only care about service_map here.
	// We keep it simple and just re-read the JSON directly.
	import_path := workspacePath + "/.devstack.json"
	data, err := os.ReadFile(import_path)
	if err != nil {
		if os.IsNotExist(err) {
			return &mutableWorkspaceConfig{raw: map[string]interface{}{}}, nil
		}
		return nil, err
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	cfg := &mutableWorkspaceConfig{raw: raw}
	// Parse existing service_map
	if sm, ok := raw["service_map"].(map[string]interface{}); ok {
		if edges, ok := sm["edges"].([]interface{}); ok {
			for _, e := range edges {
				if em, ok := e.(map[string]interface{}); ok {
					from, _ := em["from"].(string)
					to, _ := em["to"].(string)
					if from != "" && to != "" {
						cfg.ServiceMapEdges = append(cfg.ServiceMapEdges, serviceMapEdge{From: from, To: to})
					}
				}
			}
		}
	}
	return cfg, nil
}

func saveWorkspaceConfig(workspacePath string, cfg *mutableWorkspaceConfig) {
	raw := cfg.raw
	if raw == nil {
		raw = map[string]interface{}{}
	}
	edges := make([]map[string]string, 0, len(cfg.ServiceMapEdges))
	for _, e := range cfg.ServiceMapEdges {
		edges = append(edges, map[string]string{"from": e.From, "to": e.To})
	}
	raw["service_map"] = map[string]interface{}{
		"edges":      edges,
		"updated_at": cfg.ServiceMapUpdatedAt.UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(workspacePath+"/.devstack.json", data, 0644)
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
