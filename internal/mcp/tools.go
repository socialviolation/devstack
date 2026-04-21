package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"devstack/internal/config"
	"devstack/internal/observability"
	_ "devstack/internal/observability/signoz" // register signoz backend
	"devstack/internal/otel"
	"devstack/internal/tilt"
	"devstack/internal/workspace"
)

// errorRegex matches common error-indicating log keywords.
var errorRegex = regexp.MustCompile(`(?i)(error|exception|panic|fatal|fail)`)

// RegisterTools registers devstack MCP tools. Tool set depends on the active environment type:
// - Local environments get all 6 tools (status, restart, stop, configure, process_logs, investigate)
// - Remote environments get investigate + environment + remote status only
func RegisterTools(
	mcpServer *server.MCPServer,
	tiltClient *tilt.Client,
	defaultService string,
	backend observability.Backend,
	tiltfilePath string,
	activeEnvName string,
	activeEnv workspace.Environment,
	allEnvs map[string]workspace.Environment,
	workspaceName string,
	workspacePath string,
	ws *workspace.Workspace,
) {
	// Always register these tools (all environment types)
	registerInvestigateTool(mcpServer, tiltClient, defaultService, backend, activeEnvName, activeEnv, workspacePath, ws)
	registerEnvironmentTool(mcpServer, activeEnvName, activeEnv, allEnvs, workspaceName)

	if activeEnv.Type == workspace.EnvironmentTypeLocal {
		// Local-only tools: full service control
		serviceDirs := tilt.ParseTiltfileServeDirs(tiltfilePath)
		cfg, _ := config.Load(workspacePath)
		if cfg == nil {
			cfg = &config.WorkspaceConfig{
				Deps:         map[string][]string{},
				Groups:       map[string][]string{},
				ServicePaths: map[string]string{},
			}
		}
		registerStatusTool(mcpServer, tiltClient, serviceDirs, cfg)
		registerTelemetryHealthTool(mcpServer, workspacePath)
		registerRestartTool(mcpServer, tiltClient, defaultService, cfg)
		registerStopTool(mcpServer, tiltClient, cfg)
		registerConfigureTool(mcpServer, tiltClient)
		registerProcessLogsTool(mcpServer, tiltClient, defaultService, cfg)
		registerServiceEnvTool(mcpServer, tiltClient, ws, workspacePath)
	} else {
		// Remote-only tools
		registerRemoteStatusTool(mcpServer, backend, activeEnvName, activeEnv.Observability.URL)
	}
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

func registerStatusTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, serviceDirs map[string]string, cfg *config.WorkspaceConfig) {
	tool := mcp.NewTool("status",
		mcp.WithDescription("Show the current status of all services in the LOCAL dev stack (via Tilt). Status reflects current Tilt resource state — these are locally running services managed by Tilt, not production. Returns SERVICE, STATUS (idle/starting/running/building/error/disabled), PORT(S), PATH (source directory), GROUP, and last error. Also shows a groups summary. 'idle' means the service is known to Tilt but not currently running (not started yet, or was stopped). 'running' means the process is up. 'disabled' means it was explicitly stopped."),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if len(view.UiResources) == 0 {
			return mcp.NewToolResultText("Tilt is running but no services are loaded yet. It may still be starting up."), nil
		}

		// Build a map of service name -> status for the groups summary.
		svcStatus := make(map[string]string, len(view.UiResources))

		var sb strings.Builder
		sb.WriteString("Tilt is running.\n\n")
		fmt.Fprintf(&sb, "%-24s %-10s %-14s %-40s %-16s %s\n", "SERVICE", "STATUS", "PORT(S)", "PATH", "GROUP", "ERROR")
		fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 116))

		for _, r := range view.UiResources {
			status := mcpServiceStatus(r)
			svcStatus[r.Metadata.Name] = status
			ports := mcpExtractPorts(r.Status.EndpointLinks)
			lastError := ""
			if len(r.Status.BuildHistory) > 0 {
				lastError = r.Status.BuildHistory[0].Error
			}
			if len(lastError) > 50 {
				lastError = lastError[:47] + "..."
			}
			path := shortenPath(serviceDirs[r.Metadata.Name])
			group := serviceGroup(r.Metadata.Name, cfg)
			fmt.Fprintf(&sb, "%-24s %-10s %-14s %-40s %-16s %s\n", r.Metadata.Name, status, ports, path, group, lastError)
		}

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

		return mcp.NewToolResultText(sb.String()), nil
	})
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

func registerRestartTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig) {
	tool := mcp.NewTool("restart",
		mcp.WithDescription("Restart a specific service or all services in a group in the LOCAL dev stack by triggering a rebuild. Operates on local Tilt services only — service name must be exact. If neither service nor group is given, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("service",
			mcp.Description("Exact Tilt resource name or configured alias (e.g. 'api-service'). NOT a description or partial match. If omitted, uses the default service for this repo (unless group is given)."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to restart. All services in the group are restarted in parallel. Cannot be combined with service."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
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
					// Enable if disabled.
					for _, r := range view.UiResources {
						if r.Metadata.Name == svcName && r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
							tiltClient.RunCLI("enable", svcName) //nolint:errcheck
							break
						}
					}
					out, err := tiltClient.RunCLI("trigger", svcName)
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
			return mcp.NewToolResultText(fmt.Sprintf("restarted %d services in group %s: %s",
				len(members), groupName, strings.Join(successes, ", "))), nil
		}

		// Single service restart.
		if name == "" {
			name = defaultService
		}
		if name == "" {
			return mcp.NewToolResultError("no service specified and no default service configured for this repo"), nil
		}

		resolved, err := tilt.ResolveService(name, view)
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

		return mcp.NewToolResultText(fmt.Sprintf("Restarted service %q.\n%s", resolved, out)), nil
	})
}

func registerStopTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, cfg *config.WorkspaceConfig) {
	tool := mcp.NewTool("stop",
		mcp.WithDescription("Stop (disable) one service, all services in a group, or all services in the LOCAL dev stack. Operates on local Tilt services only — service name must be exact. If service is given, stops that service. If group is given, stops all services in the group. If neither is given, stops all services. Cannot specify both service and group."),
		mcp.WithString("service",
			mcp.Description("Exact Tilt resource name or alias to stop (e.g. 'api-service'). NOT a description or partial match. If omitted, all services are stopped (unless group is given)."),
		),
		mcp.WithString("group",
			mcp.Description("Group name to stop. All services in the group are stopped in parallel. Cannot be combined with service."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
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
					out, err := tiltClient.RunCLI("disable", svcName)
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
			return mcp.NewToolResultText(fmt.Sprintf("stopped %d services in group %s: %s",
				len(members), groupName, strings.Join(successes, ", "))), nil
		}

		// Single service stop.
		if name != "" {
			resolved, err := tilt.ResolveService(name, view)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out, err := tiltClient.RunCLI("disable", resolved)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to stop %q: %v\n%s", resolved, err, out)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Stopped %q.", resolved)), nil
		}

		// Stop all
		var sb strings.Builder
		var failures []string
		for _, r := range view.UiResources {
			out, err := tiltClient.RunCLI("disable", r.Metadata.Name)
			if err != nil {
				failures = append(failures, r.Metadata.Name)
				fmt.Fprintf(&sb, "FAILED %s: %v\n%s\n", r.Metadata.Name, err, out)
			} else {
				fmt.Fprintf(&sb, "Stopped %s\n", r.Metadata.Name)
			}
		}
		if len(failures) > 0 {
			return mcp.NewToolResultError(fmt.Sprintf("Some services failed to stop:\n%s", sb.String())), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Stopped %d service(s).\n%s", len(view.UiResources), sb.String())), nil
	})
}

// filterErrorLines returns only lines matching the error regex.
func filterErrorLines(raw string) []string {
	var matched []string
	for _, line := range strings.Split(raw, "\n") {
		if errorRegex.MatchString(line) {
			matched = append(matched, line)
		}
	}
	return matched
}

func registerProcessLogsTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, cfg *config.WorkspaceConfig) {
	tool := mcp.NewTool("process_logs",
		mcp.WithDescription("Fetch raw stdout/stderr from a locally running Tilt-managed service process. NOT a log search engine — fetches live process output directly from Tilt. Parameters are structured: exact service name, integer line count, boolean flags. Natural language queries are NOT accepted. Example: service='api-service' lines=100 since_restart=true. Use for services not instrumented with OTEL or when you need unstructured process output. If no service is given, uses the default or fetches all services in parallel. Supports grep filtering, paging via offset, and since_restart to isolate post-startup output. When group is given, fetches logs from all services in the group concurrently. Cannot specify both service and group."),
		mcp.WithString("service",
			mcp.Description("Exact service name or alias as registered in Tilt (e.g. 'api-service'). NOT a description or partial match. If omitted, uses the default service for this repo or fetches all."),
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
			mcp.Description("If true, return only lines since the last deploy/restart of the service. Uses the Tilt deploy timestamp — no heuristics. Defaults to true."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, return only lines matching error/exception/panic/fatal/fail. Defaults to false."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		groupName := request.GetString("group", "")

		if name != "" && groupName != "" {
			return mcp.NewToolResultError("specify either service or group, not both"), nil
		}

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
					raw, err := tiltClient.RunCLI(buildLogArgs(svcName)...)
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
			return mcp.NewToolResultText(sb.String()), nil
		}

		if name != "" {
			resolved, err := tilt.ResolveService(name, view)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, err := tiltClient.RunCLI(buildLogArgs(resolved)...)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to get logs for %q: %v\n%s", resolved, err, raw)), nil
			}
			out := processOutput(raw)
			if out == "" {
				return mcp.NewToolResultText(fmt.Sprintf("No matching output in %s.", resolved)), nil
			}
			return mcp.NewToolResultText(out), nil
		}

		// All services in parallel.
		type result struct {
			name string
			out  string
			err  error
		}
		services := make([]string, 0, len(view.UiResources))
		for _, r := range view.UiResources {
			services = append(services, r.Metadata.Name)
		}
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
		return mcp.NewToolResultText(sb.String()), nil
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

func registerConfigureTool(mcpServer *server.MCPServer, tiltClient *tilt.Client) {
	tool := mcp.NewTool("configure",
		mcp.WithDescription("Set a Tilt runtime argument (key=value) that controls how services are configured. Passed via `tilt args -- key=value`. Use this to change feature flags, modes, or other Tilt-managed config. Affected services will restart automatically."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The argument key (e.g. 'env', 'debug', 'profile')."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The value to set (e.g. 'production', 'true', 'staging')."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := request.GetString("key", "")
		value := request.GetString("value", "")
		if key == "" {
			return mcp.NewToolResultError("key must not be empty"), nil
		}

		out, err := tiltClient.RunCLI("args", "--", fmt.Sprintf("%s=%s", key, value))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set %s=%s: %v\n%s", key, value, err, out)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Set %s=%s. Tilt will restart affected services.", key, value)), nil
	})
}

func registerInvestigateTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string, backend observability.Backend, activeEnvName string, activeEnv workspace.Environment, workspacePath string, ws *workspace.Workspace) {
	// Determine the local plugin query endpoint (if any)
	var localQueryEndpoint string
	var localPluginName string
	var localPluginHasNoUI bool
	var localPluginUpstream string
	if ws != nil && activeEnv.Type == workspace.EnvironmentTypeLocal {
		plugin := otel.Get(ws.OtelPlugin)
		if plugin != nil {
			localQueryEndpoint = plugin.QueryEndpoint(ws)
			localPluginName = plugin.Name()
			if localQueryEndpoint == "" {
				localPluginHasNoUI = true
				localPluginUpstream = ws.PluginConfig("upstream")
			}
		}
	}

	var desc string
	if activeEnv.Type == workspace.EnvironmentTypeLocal {
		queryURL := localQueryEndpoint
		if queryURL == "" {
			queryURL = activeEnv.Observability.URL
		}
		desc = fmt.Sprintf(
			"Investigate distributed traces in the LOCAL dev environment (@ %s). "+
				"Queries SignOz via ClickHouse — NOT a natural language search engine. Parameters are structured: exact service names, structured time ranges, and exact attribute key=value pairs. "+
				"Modes: (1) trace_id/span_id — look up a specific trace or span; (2) attribute+value — search by business attribute (e.g. attribute='portfolio.id' value='123'); (3) service — show recent executions for a service. "+
				"Example: service='api-service' since_minutes=15 errors_only=true. "+
				"Returns an ASCII span tree showing service calls, durations, and errors. Combine with process_logs and status for full debugging context.",
			queryURL,
		)
	} else {
		desc = fmt.Sprintf(
			"Investigate distributed traces in the **%s** environment (SigNoz @ %s). "+
				"READ-ONLY — service control tools (restart/stop/configure) are not available here. "+
				"Queries SignOz via ClickHouse — NOT a natural language search engine. Parameters are structured: exact service names, structured time ranges, and exact attribute key=value pairs. "+
				"Modes: (1) trace_id/span_id — look up a specific trace or span; (2) attribute+value — search by business attribute; (3) service — show recent executions. "+
				"Returns an ASCII span tree showing service calls, durations, and errors.",
			activeEnvName, activeEnv.Observability.URL,
		)
	}

	// Suppress unused variable warnings
	_ = localPluginName
	_ = localPluginHasNoUI
	_ = localPluginUpstream

	tool := mcp.NewTool("investigate",
		mcp.WithDescription(desc),
		mcp.WithString("trace_id",
			mcp.Description("Specific trace ID to look up. If given, all other filters are ignored."),
		),
		mcp.WithString("span_id",
			mcp.Description("Specific span ID to look up. Finds the trace containing this span. Ignored if trace_id is given."),
		),
		mcp.WithString("service",
			mcp.Description("Exact service name as registered in Tilt/SignOz (e.g. 'api-service'). NOT a description or partial match. Only applied in mode 3 (no trace_id or attribute given); attribute searches and trace lookups span all services."),
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
				Since:     since,
				Limit:     limit * 5,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			traceGroups = matched
		} else {
			// Mode 3: recent executions
			if service == "" {
				service = defaultService
			}
			recent, err := backend.QueryTraces(ctx, observability.TraceQuery{
				Service: service,
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

// sortedEnvKeys returns sorted map keys for deterministic output.
func sortedEnvKeys(m map[string]workspace.Environment) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// executionDetail holds a fully fetched execution: span tree + correlated logs.
type executionDetail struct {
	record   *traceRecord
	otelLogs []logEntry
	// tiltLogs maps service name → recent process log output (fallback when OTEL logs are empty).
	tiltLogs map[string]string
}

// fetchExecutionDetails fetches the span tree and logs for a set of root trace summaries in parallel.
// For each trace it fetches: full span tree (fetchTrace) + OTEL logs (fetchLogsForTrace).
// If OTEL logs come back empty, it also fetches recent Tilt process logs from the services involved.
func fetchExecutionDetails(roots []traceRecord, otelQueryURL string, tiltClient *tilt.Client, tailLines int, verbose bool) []executionDetail {
	details := make([]executionDetail, len(roots))

	var wg sync.WaitGroup
	for i, r := range roots {
		wg.Add(1)
		go func(idx int, traceID string) {
			defer wg.Done()
			d := executionDetail{}

			// Full span tree
			if full, err := fetchTrace(otelQueryURL, traceID); err == nil && full != nil {
				d.record = full
			} else {
				// Fall back to the root-only summary we already have
				cp := roots[idx]
				d.record = &cp
			}

			// OTEL correlated logs
			d.otelLogs, _ = fetchLogsForTrace(otelQueryURL, traceID)

			// If no OTEL logs, fetch recent Tilt process logs for each involved service
			if len(d.otelLogs) == 0 && tiltClient != nil {
				d.tiltLogs = fetchTiltProcessLogs(tiltClient, d.record, tailLines, verbose)
			}

			details[idx] = d
		}(i, r.TraceID)
	}
	wg.Wait()
	return details
}

// uniqueServices returns the unique service names from a traceRecord's spans.
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
