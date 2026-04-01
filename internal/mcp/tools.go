package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"devstack/internal/tilt"
)

// errorRegex matches common error-indicating log keywords.
var errorRegex = regexp.MustCompile(`(?i)(error|exception|panic|fatal|fail)`)

// RegisterTools registers all devstack service control tools with the given MCP server.
// defaultService is used as a fallback when a tool's name argument is omitted.
// otelQueryURL is the base URL for the observability query API (e.g. http://localhost:8080
// for the managed SigNoz query-service, or a BYO query URL). If empty, trace tools degrade
// gracefully with a helpful message.
func RegisterTools(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService, otelQueryURL string) {
	registerStatusTool(mcpServer, tiltClient)
	registerRestartTool(mcpServer, tiltClient, defaultService)
	registerStopTool(mcpServer, tiltClient, defaultService)
	registerStopAllTool(mcpServer, tiltClient)
	registerLogsTool(mcpServer, tiltClient, defaultService)
	registerErrorsTool(mcpServer, tiltClient, defaultService)
	registerWhatHappenedTool(mcpServer, tiltClient, defaultService, otelQueryURL)
	registerSetEnvironmentTool(mcpServer, tiltClient)
	registerTracesTool(mcpServer, otelQueryURL)
	registerTraceDetailTool(mcpServer, otelQueryURL)
	registerTraceSearchTool(mcpServer, otelQueryURL)
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

func registerStatusTool(mcpServer *server.MCPServer, tiltClient *tilt.Client) {
	tool := mcp.NewTool("status",
		mcp.WithDescription("Show the current status of all services in the dev stack. Returns SERVICE, STATUS (idle/starting/running/building/error/disabled), PORT(S), and last error. 'idle' means the service is known to Tilt but not currently running (not started yet, or was stopped). 'running' means the process is up. 'disabled' means it was explicitly stopped."),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if len(view.UiResources) == 0 {
			return mcp.NewToolResultText("Tilt is running but no services are loaded yet. It may still be starting up."), nil
		}

		var sb strings.Builder
		sb.WriteString("Tilt is running.\n\n")
		fmt.Fprintf(&sb, "%-24s %-10s %-14s %s\n", "SERVICE", "STATUS", "PORT(S)", "ERROR")
		fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 80))

		for _, r := range view.UiResources {
			status := mcpServiceStatus(r)
			ports := mcpExtractPorts(r.Status.EndpointLinks)
			lastError := ""
			if len(r.Status.BuildHistory) > 0 {
				lastError = r.Status.BuildHistory[0].Error
			}
			if len(lastError) > 50 {
				lastError = lastError[:47] + "..."
			}
			fmt.Fprintf(&sb, "%-24s %-10s %-14s %s\n", r.Metadata.Name, status, ports, lastError)
		}

		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerRestartTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	tool := mcp.NewTool("restart",
		mcp.WithDescription("Restart a specific service in the dev stack by triggering a rebuild. If name is omitted, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("service",
			mcp.Description("The service name to restart. Can be the exact Tilt resource name or a configured alias. If omitted, uses the default service for this repo."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		if name == "" {
			name = defaultService
		}
		if name == "" {
			return mcp.NewToolResultError("no service specified and no default service configured for this repo"), nil
		}

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
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

func registerStopTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	tool := mcp.NewTool("stop",
		mcp.WithDescription("Stop (disable) a specific service in the dev stack. If name is omitted, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("service",
			mcp.Description("The service name to stop. Can be the exact Tilt resource name or a configured alias. If omitted, uses the default service for this repo."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		if name == "" {
			name = defaultService
		}
		if name == "" {
			return mcp.NewToolResultError("no service specified and no default service configured for this repo"), nil
		}

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		resolved, err := tilt.ResolveService(name, view)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out, err := tiltClient.RunCLI("disable", resolved)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to stop %q: %v\n%s", resolved, err, out)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Stopped service %q.\n%s", resolved, out)), nil
	})
}

func registerStopAllTool(mcpServer *server.MCPServer, tiltClient *tilt.Client) {
	tool := mcp.NewTool("stop_all",
		mcp.WithDescription("Stop (disable) all services in the dev stack."),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var results strings.Builder
		var failures []string
		for _, r := range view.UiResources {
			out, err := tiltClient.RunCLI("disable", r.Metadata.Name)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", r.Metadata.Name, err))
				fmt.Fprintf(&results, "FAILED %s: %v\n%s\n", r.Metadata.Name, err, out)
			} else {
				fmt.Fprintf(&results, "Stopped %s\n", r.Metadata.Name)
			}
		}

		if len(failures) > 0 {
			return mcp.NewToolResultError(fmt.Sprintf("Some services failed to stop:\n%s", results.String())), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Stopped %d service(s):\n%s", len(view.UiResources), results.String())), nil
	})
}

func registerLogsTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	tool := mcp.NewTool("logs",
		mcp.WithDescription("Fetch recent log output for a specific service in the dev stack. If name is omitted, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("service",
			mcp.Description("The service name. Can be the exact Tilt resource name or a configured alias. If omitted, uses the default service for this repo."),
		),
		mcp.WithNumber("lines",
			mcp.Description("Number of log lines to return. Defaults to 100."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		if name == "" {
			name = defaultService
		}
		if name == "" {
			return mcp.NewToolResultError("no service specified and no default service configured for this repo"), nil
		}
		lines := int(request.GetFloat("lines", 100))

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		resolved, err := tilt.ResolveService(name, view)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out, err := tiltClient.RunCLI("logs", fmt.Sprintf("--tail=%d", lines), resolved)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get logs for %q: %v\n%s", resolved, err, out)), nil
		}

		return mcp.NewToolResultText(out), nil
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

func registerErrorsTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	tool := mcp.NewTool("errors",
		mcp.WithDescription("Get error lines from service logs. If no service name is given and a default service is configured (DEVSTACK_DEFAULT_SERVICE), scans that service. Otherwise scans all services in parallel."),
		mcp.WithString("service",
			mcp.Description("Optional service name. If empty and a default service is set for this repo, uses that. Otherwise all services are scanned."),
		),
		mcp.WithNumber("lines",
			mcp.Description("Number of log lines to fetch per service before filtering. Defaults to 50."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		if name == "" {
			name = defaultService
		}
		lines := int(request.GetFloat("lines", 50))

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if name != "" {
			// Single service
			resolved, err := tilt.ResolveService(name, view)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, err := tiltClient.RunCLI("logs", fmt.Sprintf("--tail=%d", lines), resolved)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to get logs for %q: %v\n%s", resolved, err, raw)), nil
			}
			filtered := filterErrorLines(raw)
			if len(filtered) == 0 {
				return mcp.NewToolResultText(fmt.Sprintf("No errors found in %s.", resolved)), nil
			}
			return mcp.NewToolResultText(strings.Join(filtered, "\n")), nil
		}

		// All services in parallel
		type result struct {
			name  string
			lines []string
			err   error
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
				raw, err := tiltClient.RunCLI("logs", fmt.Sprintf("--tail=%d", lines), svcName)
				results[idx] = result{
					name:  svcName,
					lines: filterErrorLines(raw),
					err:   err,
				}
			}(i, svc)
		}
		wg.Wait()

		var sb strings.Builder
		for _, r := range results {
			fmt.Fprintf(&sb, "=== %s ===\n", r.name)
			if r.err != nil {
				fmt.Fprintf(&sb, "ERROR fetching logs: %v\n\n", r.err)
				continue
			}
			if len(r.lines) == 0 {
				sb.WriteString("No errors found.\n\n")
			} else {
				sb.WriteString(strings.Join(r.lines, "\n"))
				sb.WriteString("\n\n")
			}
		}

		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerSetEnvironmentTool(mcpServer *server.MCPServer, tiltClient *tilt.Client) {
	tool := mcp.NewTool("set_environment",
		mcp.WithDescription("Pass an arbitrary key=value argument to Tilt via `tilt args -- key=value`. Use this to change runtime configuration (e.g. environment, feature flags) for services managed by Tilt. Tilt will restart affected services automatically."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The argument key to set (e.g. 'env', 'debug', 'profile')."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The value to assign to the key (e.g. 'production', 'true', 'staging')."),
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

func registerTracesTool(mcpServer *server.MCPServer, otelQueryURL string) {
	tool := mcp.NewTool("traces",
		mcp.WithDescription("List recent traces from the observability backend (SigNoz). Returns one row per distinct execution (root spans only — entry points, not internal child spans): timestamp, trace ID, operation, service, duration, status (ok/error). Use this to see what requests or jobs ran recently. Follow up with trace_detail <trace_id> to see the full execution view including span tree and correlated logs."),
		mcp.WithString("service",
			mcp.Description("Optional service name filter (e.g. 'navexa-api'). If omitted, queries all services."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of traces to return. Defaults to 20."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Look-back window in minutes. Defaults to 60."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if otelQueryURL == "" {
			return mcp.NewToolResultError("No observability query endpoint configured. Set one with: devstack otel set-endpoint <otlp-url> --query-url=<query-url>"), nil
		}

		service := request.GetString("service", "")
		limit := int(request.GetFloat("limit", 20))
		sinceMinutes := int(request.GetFloat("since_minutes", 60))

		traces, err := fetchRootTraces(otelQueryURL, service, limit, sinceMinutes)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(formatTraceList(traces)), nil
	})
}

func registerTraceDetailTool(mcpServer *server.MCPServer, otelQueryURL string) {
	tool := mcp.NewTool("trace_detail",
		mcp.WithDescription("Get the full execution view for a specific trace: complete span tree across all services (with durations, statuses, business attributes) plus any correlated log records exported via OTEL. This is the single-pane view of one execution across the whole system. Use after identifying a trace_id from `traces` or `trace_search`."),
		mcp.WithString("trace_id",
			mcp.Required(),
			mcp.Description("The trace ID to fetch. Get this from the `traces` or `trace_search` tools."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if otelQueryURL == "" {
			return mcp.NewToolResultError("No observability query endpoint configured. Set one with: devstack otel set-endpoint <otlp-url> --query-url=<query-url>"), nil
		}

		traceID := request.GetString("trace_id", "")
		if traceID == "" {
			return mcp.NewToolResultError("trace_id is required"), nil
		}

		record, err := fetchTrace(otelQueryURL, traceID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if record == nil {
			return mcp.NewToolResultText(fmt.Sprintf("Trace %q not found.", traceID)), nil
		}

		// Best-effort: fetch correlated logs by traceID; empty is fine if services don't export logs via OTEL.
		logs, _ := fetchLogsForTrace(otelQueryURL, traceID)

		return mcp.NewToolResultText(formatExecutionView(record, logs)), nil
	})
}

func registerTraceSearchTool(mcpServer *server.MCPServer, otelQueryURL string) {
	tool := mcp.NewTool("trace_search",
		mcp.WithDescription("Search for traces by a business attribute (e.g. portfolio.id, user.id, process.id). Returns matching traces. Use this to find all activity related to a specific user, portfolio, or import job. Filters server-side via SigNoz query API."),
		mcp.WithString("attribute",
			mcp.Required(),
			mcp.Description("The attribute key to search for (e.g. 'portfolio.id', 'user.id', 'process.id')."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The attribute value to match (e.g. '123', 'user-abc')."),
		),
		mcp.WithString("service",
			mcp.Description("Optional service name to narrow the search. If omitted, all services are searched."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of matching traces to return. Defaults to 10."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Look-back window in minutes. Defaults to 60."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if otelQueryURL == "" {
			return mcp.NewToolResultError("No observability query endpoint configured. Set one with: devstack otel set-endpoint <otlp-url> --query-url=<query-url>"), nil
		}

		attribute := request.GetString("attribute", "")
		value := request.GetString("value", "")
		service := request.GetString("service", "")
		limit := int(request.GetFloat("limit", 10))
		sinceMinutes := int(request.GetFloat("since_minutes", 60))

		if attribute == "" || value == "" {
			return mcp.NewToolResultError("attribute and value are both required"), nil
		}

		matched, err := searchTraces(otelQueryURL, attribute, value, service, limit, sinceMinutes)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if len(matched) == 0 {
			svcMsg := ""
			if service != "" {
				svcMsg = fmt.Sprintf(" in service %q", service)
			}
			return mcp.NewToolResultText(fmt.Sprintf("No traces found where %s=%s%s.", attribute, value, svcMsg)), nil
		}

		return mcp.NewToolResultText(formatTraceList(matched)), nil
	})
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
func fetchExecutionDetails(roots []traceRecord, otelQueryURL string, tiltClient *tilt.Client) []executionDetail {
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
				services := uniqueServices(d.record)
				if len(services) > 0 {
					d.tiltLogs = fetchTiltProcessLogs(tiltClient, services, 80)
				}
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

// fetchTiltProcessLogs fetches recent stdout logs from Tilt for each service.
func fetchTiltProcessLogs(tiltClient *tilt.Client, services []string, tailLines int) map[string]string {
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
func formatExecutionDetailView(d *executionDetail) string {
	var sb strings.Builder

	// Span tree + OTEL header/logs
	sb.WriteString(formatExecutionView(d.record, d.otelLogs))

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

func registerWhatHappenedTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService, otelQueryURL string) {
	tool := mcp.NewTool("what_happened",
		mcp.WithDescription("Find the most recent executions and show a complete correlated investigation view for each: full span tree across all services, durations, errors, business attributes (portfolio.id, user.id, etc.), and correlated logs. Call this after triggering a request — it finds what just ran and gives you everything needed to map the execution to source code and explain what went wrong. Defaults to the 3 most recent executions in the past 5 minutes."),
		mcp.WithString("service",
			mcp.Description("Optional service name filter. If omitted and a default service is configured, uses that."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Number of recent executions to show. Defaults to 3."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Look-back window in minutes. Defaults to 5."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, only show executions where the root span has an error status. Defaults to false."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if otelQueryURL == "" {
			return mcp.NewToolResultError("No observability query endpoint configured. Set one with: devstack otel set-endpoint <otlp-url> --query-url=<query-url>"), nil
		}

		service := request.GetString("service", "")
		if service == "" {
			service = defaultService
		}
		limit := int(request.GetFloat("limit", 3))
		sinceMinutes := int(request.GetFloat("since_minutes", 5))
		errorsOnly := request.GetBool("errors_only", false)

		// Fetch more root spans than needed so we have room to filter.
		fetchLimit := limit * 5
		if fetchLimit < 10 {
			fetchLimit = 10
		}
		roots, err := fetchRootTraces(otelQueryURL, service, fetchLimit, sinceMinutes)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if errorsOnly {
			filtered := roots[:0]
			for _, r := range roots {
				root := rootSpan(&r)
				if root != nil && spanHasError(root) {
					filtered = append(filtered, r)
				}
			}
			roots = filtered
		}

		if len(roots) == 0 {
			msg := fmt.Sprintf("No traces found in the last %d minute(s).", sinceMinutes)
			if service != "" {
				msg = fmt.Sprintf("No traces found for %q in the last %d minute(s).", service, sinceMinutes)
			}
			return mcp.NewToolResultText(msg), nil
		}

		if len(roots) > limit {
			roots = roots[:limit]
		}

		details := fetchExecutionDetails(roots, otelQueryURL, tiltClient)

		sep := "\n" + strings.Repeat("─", 60) + "\n\n"
		var sb strings.Builder
		for i := range details {
			if i > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(formatExecutionDetailView(&details[i]))
		}

		return mcp.NewToolResultText(sb.String()), nil
	})
}
