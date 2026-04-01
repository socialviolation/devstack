package mcp

import (
	"context"
	"fmt"
	"os"
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
// otelQueryURL is the SigNoz query API base URL (e.g. http://localhost:3301).
// If empty, the investigate tool returns an error.
// tiltfilePath is the path to the workspace Tiltfile; used to show code locations in status.
func RegisterTools(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService, otelQueryURL, tiltfilePath string) {
	serviceDirs := tilt.ParseTiltfileServeDirs(tiltfilePath)
	registerStatusTool(mcpServer, tiltClient, serviceDirs)
	registerRestartTool(mcpServer, tiltClient, defaultService)
	registerStopTool(mcpServer, tiltClient)
	registerConfigureTool(mcpServer, tiltClient)
	registerProcessLogsTool(mcpServer, tiltClient, defaultService)
	registerInvestigateTool(mcpServer, tiltClient, defaultService, otelQueryURL)
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

func registerStatusTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, serviceDirs map[string]string) {
	tool := mcp.NewTool("status",
		mcp.WithDescription("Show the current status of all services in the dev stack. Returns SERVICE, STATUS (idle/starting/running/building/error/disabled), PORT(S), PATH (source directory), and last error. 'idle' means the service is known to Tilt but not currently running (not started yet, or was stopped). 'running' means the process is up. 'disabled' means it was explicitly stopped."),
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
		fmt.Fprintf(&sb, "%-24s %-10s %-14s %-40s %s\n", "SERVICE", "STATUS", "PORT(S)", "PATH", "ERROR")
		fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 100))

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
			path := shortenPath(serviceDirs[r.Metadata.Name])
			fmt.Fprintf(&sb, "%-24s %-10s %-14s %-40s %s\n", r.Metadata.Name, status, ports, path, lastError)
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

func registerStopTool(mcpServer *server.MCPServer, tiltClient *tilt.Client) {
	tool := mcp.NewTool("stop",
		mcp.WithDescription("Stop (disable) one or all services. If service is given, stops that service. If omitted, stops all services."),
		mcp.WithString("service",
			mcp.Description("Service name or alias to stop. If omitted, all services are stopped."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

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

func registerProcessLogsTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	tool := mcp.NewTool("process_logs",
		mcp.WithDescription("Fetch raw stdout/stderr from a service process via Tilt. Use this for services not instrumented with OTEL, or when you need unstructured process output. If no service is given, uses the default or fetches all services in parallel."),
		mcp.WithString("service",
			mcp.Description("Service name or alias. If omitted, uses the default service for this repo or fetches all."),
		),
		mcp.WithNumber("lines",
			mcp.Description("Number of lines to fetch per service. Defaults to 100."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, return only lines matching error/exception/panic/fatal/fail. Defaults to false."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		if name == "" {
			name = defaultService
		}
		lines := int(request.GetFloat("lines", 100))
		errorsOnly := request.GetBool("errors_only", false)

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		emit := func(raw string) string {
			if errorsOnly {
				filtered := filterErrorLines(raw)
				if len(filtered) == 0 {
					return ""
				}
				return strings.Join(filtered, "\n")
			}
			return raw
		}

		if name != "" {
			resolved, err := tilt.ResolveService(name, view)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, err := tiltClient.RunCLI("logs", fmt.Sprintf("--tail=%d", lines), resolved)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to get logs for %q: %v\n%s", resolved, err, raw)), nil
			}
			out := emit(raw)
			if out == "" {
				return mcp.NewToolResultText(fmt.Sprintf("No matching output in %s.", resolved)), nil
			}
			return mcp.NewToolResultText(out), nil
		}

		// All services in parallel
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
				raw, err := tiltClient.RunCLI("logs", fmt.Sprintf("--tail=%d", lines), svcName)
				results[idx] = result{name: svcName, out: emit(raw), err: err}
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

func registerInvestigateTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService, otelQueryURL string) {
	tool := mcp.NewTool("investigate",
		mcp.WithDescription("Investigate executions in the observability backend. Three modes: (1) trace_id given → full execution view for that specific trace; (2) attribute+value given → find executions matching that business attribute (e.g. portfolio.id=123) and expand each; (3) neither → find the most recent executions and expand each. Each result shows the full cross-service span tree, durations, errors, business attributes, and correlated logs. Falls back to process stdout when OTEL logs are unavailable. Use errors_only=true to filter to failed executions only. Use this as the primary diagnostic tool."),
		mcp.WithString("trace_id",
			mcp.Description("Specific trace ID to look up. If given, all other filters are ignored."),
		),
		mcp.WithString("service",
			mcp.Description("Filter by service name. Only applied when browsing recent executions (mode 3 — no trace_id or attribute given). Attribute searches and trace lookups always span all services."),
		),
		mcp.WithString("attribute",
			mcp.Description("Business attribute key to search by (e.g. 'portfolio.id', 'user.id', 'process.id'). Requires value."),
		),
		mcp.WithString("value",
			mcp.Description("Value to match for the given attribute (e.g. '123')."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Look-back window in minutes. Defaults to 5."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of executions to expand. Defaults to 3."),
		),
		mcp.WithBoolean("errors_only",
			mcp.Description("If true, only return executions where the root span has error status. Defaults to false."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if otelQueryURL == "" {
			return mcp.NewToolResultError("SigNoz is not running. Start it with: devstack otel start"), nil
		}

		traceID := request.GetString("trace_id", "")
		service := request.GetString("service", "")
		attribute := request.GetString("attribute", "")
		value := request.GetString("value", "")
		sinceMinutes := int(request.GetFloat("since_minutes", 5))
		limit := int(request.GetFloat("limit", 3))
		errorsOnly := request.GetBool("errors_only", false)

		// Mode 1: specific trace ID
		if traceID != "" {
			record, err := fetchTrace(otelQueryURL, traceID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if record == nil {
				return mcp.NewToolResultText(fmt.Sprintf("Trace %q not found.", traceID)), nil
			}
			logs, _ := fetchLogsForTrace(otelQueryURL, traceID)
			d := executionDetail{record: record, otelLogs: logs}
			if len(logs) == 0 {
				d.tiltLogs = fetchTiltProcessLogs(tiltClient, uniqueServices(record), 80)
			}
			return mcp.NewToolResultText(formatExecutionDetailView(&d)), nil
		}

		// Mode 2: attribute search
		var roots []traceRecord
		if attribute != "" && value != "" {
			fetchLimit := limit * 5
			if fetchLimit < 10 {
				fetchLimit = 10
			}
			matched, err := searchTraces(otelQueryURL, attribute, value, service, fetchLimit, sinceMinutes)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			roots = matched
		} else {
			// Mode 3: recent executions — apply default service scope if no explicit service given
			if service == "" {
				service = defaultService
			}
			fetchLimit := limit * 5
			if fetchLimit < 10 {
				fetchLimit = 10
			}
			var err error
			roots, err = fetchRootTraces(otelQueryURL, service, fetchLimit, sinceMinutes)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
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

