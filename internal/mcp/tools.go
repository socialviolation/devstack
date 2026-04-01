package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

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
		mcp.WithDescription("List recent traces from the observability backend (SigNoz). Returns a table of recent request traces: timestamp, trace ID, root operation, service, duration, and status (ok/error). Use this to see recent activity for a service or the whole system."),
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

		traces, err := fetchTraces(otelQueryURL, service, limit, sinceMinutes)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(formatTraceList(traces)), nil
	})
}

func registerTraceDetailTool(mcpServer *server.MCPServer, otelQueryURL string) {
	tool := mcp.NewTool("trace_detail",
		mcp.WithDescription("Get the full span tree for a specific trace. Shows every span with its service, operation, duration, status, and business attributes (portfolio.id, user.id, etc.). Use this after identifying a trace_id from the `traces` or `trace_search` tools."),
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

		return mcp.NewToolResultText(formatSpanTree(record)), nil
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

type serviceAnalysis struct {
	name      string
	logErrors []string
	logErr    error
	traces    []traceRecord
	traceErr  error
}

func analyseService(svcName string, tiltClient *tilt.Client, otelQueryURL string) serviceAnalysis {
	a := serviceAnalysis{name: svcName}

	var wg sync.WaitGroup
	wg.Add(2)

	// Fetch log errors from Tilt
	go func() {
		defer wg.Done()
		raw, err := tiltClient.RunCLI("logs", "--tail=500", svcName)
		if err != nil {
			a.logErr = err
			return
		}
		a.logErrors = filterErrorLines(raw)
	}()

	// Fetch traces from observability backend
	go func() {
		defer wg.Done()
		if otelQueryURL == "" {
			a.traceErr = fmt.Errorf("no observability query endpoint configured")
			return
		}
		records, err := fetchTraces(otelQueryURL, svcName, 50, 15)
		if err != nil {
			a.traceErr = err
			return
		}
		a.traces = records
	}()

	wg.Wait()
	return a
}

func formatServiceAnalysis(a *serviceAnalysis, sinceMinutes int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "=== %s (last %d min) ===\n\n", a.name, sinceMinutes)

	// --- Traces section ---
	if a.traceErr != nil {
		fmt.Fprintf(&sb, "TRACES — unavailable (%v)\n\n", a.traceErr)
	} else if len(a.traces) == 0 {
		sb.WriteString("TRACES — no data (service may not be instrumented or observability backend is empty)\n\n")
	} else {
		var errTraces []traceRecord
		okCount := 0
		for _, t := range a.traces {
			hasErr := false
			for i := range t.Spans {
				if spanHasError(&t.Spans[i]) {
					hasErr = true
					break
				}
			}
			if hasErr {
				errTraces = append(errTraces, t)
			} else {
				okCount++
			}
		}

		fmt.Fprintf(&sb, "TRACES — %d errors, %d ok\n", len(errTraces), okCount)

		for _, t := range errTraces {
			root := rootSpan(&t)
			if root == nil {
				continue
			}
			ts := time.Unix(0, root.StartNs).Local().Format("15:04:05")
			traceShort := t.TraceID
			if len(traceShort) > 16 {
				traceShort = traceShort[:16] + ".."
			}
			durationMs := float64(root.DurationNs) / 1e6
			fmt.Fprintf(&sb, "  [ERROR] %s  %s  %s  %.1fms\n", ts, traceShort, root.Operation, durationMs)

			// Print business attributes and error details from the failing span(s)
			for i := range t.Spans {
				sp := &t.Spans[i]
				if !spanHasError(sp) {
					continue
				}
				for k, v := range sp.Attrs {
					switch k {
					case "portfolio.id", "user.id", "process.id", "file.type", "provider.id":
						fmt.Fprintf(&sb, "          %s: %s\n", k, v)
					case "exception.message", "error.message", "otel.status_description":
						fmt.Fprintf(&sb, "          error: %s\n", v)
					}
				}
				if sp.StatusMsg != "" {
					fmt.Fprintf(&sb, "          status: %s\n", sp.StatusMsg)
				}
			}
		}
		sb.WriteString("\n")
	}

	// --- Log errors section ---
	if a.logErr != nil {
		fmt.Fprintf(&sb, "LOG ERRORS — unavailable (%v)\n\n", a.logErr)
	} else if len(a.logErrors) == 0 {
		sb.WriteString("LOG ERRORS — none\n\n")
	} else {
		fmt.Fprintf(&sb, "LOG ERRORS — %d lines\n", len(a.logErrors))
		for _, line := range a.logErrors {
			fmt.Fprintf(&sb, "  %s\n", line)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func registerWhatHappenedTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService, otelQueryURL string) {
	tool := mcp.NewTool("what_happened",
		mcp.WithDescription("Diagnose recent errors across services by correlating both log output and distributed traces. For each service, shows: TRACES (error counts, failing operations, business attributes like portfolio.id/user.id, and error messages from the observability backend) and LOG ERRORS (exception/panic/error lines from stdout). Use this as the first diagnostic tool — it gives a complete picture without needing to call traces and logs separately."),
		mcp.WithString("service",
			mcp.Description("Optional service name. If empty and a default service is set for this repo, uses that. Otherwise all services are scanned."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Time window to look back. Defaults to 15."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("service", "")
		if name == "" {
			name = defaultService
		}
		sinceMinutes := int(request.GetFloat("since_minutes", 15))

		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var services []string
		if name != "" {
			resolved, err := tilt.ResolveService(name, view)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			services = []string{resolved}
		} else {
			for _, r := range view.UiResources {
				services = append(services, r.Metadata.Name)
			}
		}

		analyses := make([]serviceAnalysis, len(services))
		var wg sync.WaitGroup
		for i, svc := range services {
			wg.Add(1)
			go func(idx int, svcName string) {
				defer wg.Done()
				analyses[idx] = analyseService(svcName, tiltClient, otelQueryURL)
			}(i, svc)
		}
		wg.Wait()

		var sb strings.Builder
		for i := range analyses {
			sb.WriteString(formatServiceAnalysis(&analyses[i], sinceMinutes))
		}

		return mcp.NewToolResultText(sb.String()), nil
	})
}
