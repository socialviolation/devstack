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
func RegisterTools(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	registerStatusTool(mcpServer, tiltClient)
	registerStartTool(mcpServer, tiltClient, defaultService)
	registerRestartTool(mcpServer, tiltClient, defaultService)
	registerStopTool(mcpServer, tiltClient, defaultService)
	registerStartAllTool(mcpServer, tiltClient)
	registerStopAllTool(mcpServer, tiltClient)
	registerLogsTool(mcpServer, tiltClient, defaultService)
	registerErrorsTool(mcpServer, tiltClient, defaultService)
	registerWhatHappenedTool(mcpServer, tiltClient, defaultService)
	registerSetEnvironmentTool(mcpServer, tiltClient)
	registerTracesTool(mcpServer)
	registerTraceDetailTool(mcpServer)
	registerTraceSearchTool(mcpServer)
}

func registerStatusTool(mcpServer *server.MCPServer, tiltClient *tilt.Client) {
	tool := mcp.NewTool("status",
		mcp.WithDescription("Show the current status of all services in the dev stack managed by Tilt. Returns a table of service name, build status, runtime status, and last error. For system-wide status across all workspaces, run `devstack status` from the terminal."),
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
		sb.WriteString("Tilt is running.\n")
		sb.WriteString("STATUS 'idle' = service not yet started (manual trigger mode). Use `devstack start <service>` or `devstack start --group=<name>` from the shell.\n\n")
		fmt.Fprintf(&sb, "%-24s %-14s %-14s %s\n", "SERVICE", "BUILD", "RUNTIME", "ERROR")
		fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 80))

		for _, r := range view.UiResources {
			buildStatus := r.Status.UpdateStatus
			if buildStatus == "" {
				buildStatus = "none"
			}
			runtimeStatus := r.Status.RuntimeStatus
			if runtimeStatus == "" {
				runtimeStatus = "none"
			}
			lastError := ""
			if len(r.Status.BuildHistory) > 0 {
				lastError = r.Status.BuildHistory[0].Error
			}
			if len(lastError) > 60 {
				lastError = lastError[:57] + "..."
			}
			fmt.Fprintf(&sb, "%-24s %-14s %-14s %s\n", r.Metadata.Name, buildStatus, runtimeStatus, lastError)
		}

		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerStartTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	tool := mcp.NewTool("start",
		mcp.WithDescription("Start (trigger a build/run for) a specific service in the dev stack. If name is omitted, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("name",
			mcp.Description("The service name to start. Can be the exact Tilt resource name or a configured alias. If omitted, uses the default service for this repo."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("name", "")
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

		out, err := tiltClient.RunCLI("trigger", resolved)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to start %q: %v\n%s", resolved, err, out)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Started service %q.\n%s", resolved, out)), nil
	})
}

func registerRestartTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	tool := mcp.NewTool("restart",
		mcp.WithDescription("Restart a specific service in the dev stack by triggering a rebuild. If name is omitted, uses the default service for this repo (set via DEVSTACK_DEFAULT_SERVICE)."),
		mcp.WithString("name",
			mcp.Description("The service name to restart. Can be the exact Tilt resource name or a configured alias. If omitted, uses the default service for this repo."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("name", "")
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
		mcp.WithString("name",
			mcp.Description("The service name to stop. Can be the exact Tilt resource name or a configured alias. If omitted, uses the default service for this repo."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("name", "")
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

func registerStartAllTool(mcpServer *server.MCPServer, tiltClient *tilt.Client) {
	tool := mcp.NewTool("start_all",
		mcp.WithDescription("Start (trigger) all services in the dev stack, or a specific subset. Optionally provide a comma-separated list of service names to start only those services."),
		mcp.WithString("services",
			mcp.Description("Optional comma-separated list of service names to start. If empty, all services are started."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		view, err := tiltClient.GetView()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		servicesArg := request.GetString("services", "")

		var toStart []string
		if servicesArg == "" {
			// Start all services
			if len(view.UiResources) == 0 {
				return mcp.NewToolResultText("Tilt is running but no services are loaded yet. It may still be starting up."), nil
			}
			for _, r := range view.UiResources {
				toStart = append(toStart, r.Metadata.Name)
			}
		} else {
			// Resolve each specified service
			for _, s := range strings.Split(servicesArg, ",") {
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				resolved, err := tilt.ResolveService(s, view)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				toStart = append(toStart, resolved)
			}
		}

		if len(toStart) == 0 {
			return mcp.NewToolResultText("No services to start."), nil
		}

		var results strings.Builder
		var failures []string
		for _, svc := range toStart {
			out, err := tiltClient.RunCLI("trigger", svc)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", svc, err))
				fmt.Fprintf(&results, "FAILED %s: %v\n%s\n", svc, err, out)
			} else {
				fmt.Fprintf(&results, "Started %s\n", svc)
			}
		}

		if len(failures) > 0 {
			return mcp.NewToolResultError(fmt.Sprintf("Some services failed to start:\n%s", results.String())), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Started %d service(s):\n%s", len(toStart), results.String())), nil
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
		mcp.WithString("name",
			mcp.Description("The service name. Can be the exact Tilt resource name or a configured alias. If omitted, uses the default service for this repo."),
		),
		mcp.WithNumber("lines",
			mcp.Description("Number of log lines to return. Defaults to 100."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("name", "")
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
		mcp.WithString("name",
			mcp.Description("Optional service name. If empty and a default service is set for this repo, uses that. Otherwise all services are scanned."),
		),
		mcp.WithNumber("lines",
			mcp.Description("Number of log lines to fetch per service before filtering. Defaults to 50."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("name", "")
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

func registerTracesTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("traces",
		mcp.WithDescription("List recent traces from Jaeger. Returns a table of recent request traces: timestamp, trace ID, root operation, service, duration, and status (ok/error). Use this to see recent activity for a service or the whole system."),
		mcp.WithString("service",
			mcp.Description("Optional service name filter (e.g. 'navexa-api'). If omitted, queries all services."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of traces to return. Defaults to 20."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Look back this many minutes. Defaults to 30."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		service := request.GetString("service", "")
		limit := int(request.GetFloat("limit", 20))
		since := int(request.GetFloat("since_minutes", 30))

		timeWindow := timeWindowParams(since)

		if service != "" {
			params := fmt.Sprintf("service=%s&limit=%d&%s", service, limit, timeWindow)
			result, err := jaegerGetTraces(params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(formatTraceList(result.Data)), nil
		}

		// No service: query all services in parallel
		services, err := jaegerGetServices()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(services) == 0 {
			return mcp.NewToolResultText("No services found in Jaeger. Make sure services are running and sending traces."), nil
		}

		type svcResult struct {
			traces []jaegerTrace
			err    error
		}
		results := make([]svcResult, len(services))
		var wg sync.WaitGroup
		for i, svc := range services {
			wg.Add(1)
			go func(idx int, s string) {
				defer wg.Done()
				params := fmt.Sprintf("service=%s&limit=%d&%s", s, limit, timeWindow)
				r, err := jaegerGetTraces(params)
				if err != nil {
					results[idx] = svcResult{err: err}
					return
				}
				results[idx] = svcResult{traces: r.Data}
			}(i, svc)
		}
		wg.Wait()

		var all []jaegerTrace
		for _, r := range results {
			if r.err == nil {
				all = append(all, r.traces...)
			}
		}

		// Sort all traces by start time descending
		for i := range all {
			root := findRootSpan(&all[i])
			if root == nil {
				continue
			}
			_ = root
		}

		if len(all) > limit {
			all = all[:limit]
		}

		return mcp.NewToolResultText(formatTraceList(all)), nil
	})
}

func registerTraceDetailTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("trace_detail",
		mcp.WithDescription("Get the full span tree for a specific trace. Shows every span with its service, operation, duration, status, and business attributes (portfolio.id, user.id, etc.). Use this after identifying a trace_id from the `traces` or `trace_search` tools."),
		mcp.WithString("trace_id",
			mcp.Required(),
			mcp.Description("The trace ID to fetch. Get this from the `traces` or `trace_search` tools."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		traceID := request.GetString("trace_id", "")
		if traceID == "" {
			return mcp.NewToolResultError("trace_id is required"), nil
		}

		url := fmt.Sprintf("%s/api/traces/%s", jaegerQueryURL, traceID)
		var result jaegerResponse
		if err := jaegerGet(url, &result); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if len(result.Data) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("Trace %q not found in Jaeger.", traceID)), nil
		}

		return mcp.NewToolResultText(formatSpanTree(&result.Data[0])), nil
	})
}

func registerTraceSearchTool(mcpServer *server.MCPServer) {
	tool := mcp.NewTool("trace_search",
		mcp.WithDescription("Search for traces by a business attribute (e.g. portfolio.id, user.id, process.id). Returns matching traces. Use this to find all activity related to a specific user, portfolio, or import job."),
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
			mcp.Description("Look back this many minutes. Defaults to 60."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		attribute := request.GetString("attribute", "")
		value := request.GetString("value", "")
		service := request.GetString("service", "")
		limit := int(request.GetFloat("limit", 10))
		since := int(request.GetFloat("since_minutes", 60))

		if attribute == "" || value == "" {
			return mcp.NewToolResultError("attribute and value are both required"), nil
		}

		// Jaeger tags filter: JSON object {"key": "value"}
		tagsJSON := fmt.Sprintf(`{"%s":"%s"}`, attribute, value)
		timeWindow := timeWindowParams(since)

		searchWithService := func(svc string) ([]jaegerTrace, error) {
			params := fmt.Sprintf("service=%s&tags=%s&limit=%d&%s", svc, tagsJSON, limit, timeWindow)
			result, err := jaegerGetTraces(params)
			if err != nil {
				return nil, err
			}
			return result.Data, nil
		}

		if service != "" {
			traces, err := searchWithService(service)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(traces) == 0 {
				return mcp.NewToolResultText(fmt.Sprintf("No traces found where %s=%s in service %q (last %d min).", attribute, value, service, since)), nil
			}
			return mcp.NewToolResultText(formatTraceList(traces)), nil
		}

		// Search all services in parallel
		services, err := jaegerGetServices()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		type svcResult struct {
			traces []jaegerTrace
			err    error
		}
		results := make([]svcResult, len(services))
		var wg sync.WaitGroup
		for i, svc := range services {
			wg.Add(1)
			go func(idx int, s string) {
				defer wg.Done()
				t, err := searchWithService(s)
				results[idx] = svcResult{traces: t, err: err}
			}(i, svc)
		}
		wg.Wait()

		var all []jaegerTrace
		for _, r := range results {
			if r.err == nil {
				all = append(all, r.traces...)
			}
		}

		if len(all) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("No traces found where %s=%s (last %d min).", attribute, value, since)), nil
		}
		if len(all) > limit {
			all = all[:limit]
		}

		return mcp.NewToolResultText(formatTraceList(all)), nil
	})
}

type serviceAnalysis struct {
	name        string
	logErrors   []string
	logErr      error
	traces      []jaegerTrace
	traceErr    error
}

func analyseService(svcName string, tiltClient *tilt.Client, sinceMinutes int) serviceAnalysis {
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

	// Fetch traces from Jaeger
	go func() {
		defer wg.Done()
		params := fmt.Sprintf("service=%s&limit=50&%s", svcName, timeWindowParams(sinceMinutes))
		result, err := jaegerGetTraces(params)
		if err != nil {
			a.traceErr = err
			return
		}
		a.traces = result.Data
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
		sb.WriteString("TRACES — no data (service may not be instrumented or Jaeger is empty)\n\n")
	} else {
		var errTraces []jaegerTrace
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
			root := findRootSpan(&t)
			if root == nil {
				continue
			}
			ts := time.UnixMicro(root.StartTime).Local().Format("15:04:05")
			traceShort := t.TraceID
			if len(traceShort) > 16 {
				traceShort = traceShort[:16] + ".."
			}
			durationMs := float64(root.Duration) / 1000.0
			fmt.Fprintf(&sb, "  [ERROR] %s  %s  %s  %.1fms\n", ts, traceShort, root.OperationName, durationMs)

			// Print business attributes and error details from the failing span(s)
			for i := range t.Spans {
				sp := &t.Spans[i]
				if !spanHasError(sp) {
					continue
				}
				for _, tag := range sp.Tags {
					switch tag.Key {
					case "portfolio.id", "user.id", "process.id", "file.type", "provider.id":
						fmt.Fprintf(&sb, "          %s: %v\n", tag.Key, tag.Value)
					case "exception.message", "error.message", "otel.status_description":
						fmt.Fprintf(&sb, "          error: %v\n", tag.Value)
					}
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

func registerWhatHappenedTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, defaultService string) {
	tool := mcp.NewTool("what_happened",
		mcp.WithDescription("Diagnose recent errors across services by correlating both log output and distributed traces. For each service, shows: TRACES (error counts, failing operations, business attributes like portfolio.id/user.id, and error messages from Jaeger) and LOG ERRORS (exception/panic/error lines from stdout). Use this as the first diagnostic tool — it gives a complete picture without needing to call traces and logs separately."),
		mcp.WithString("name",
			mcp.Description("Optional service name. If empty and a default service is set for this repo, uses that. Otherwise all services are scanned."),
		),
		mcp.WithNumber("since_minutes",
			mcp.Description("Time window to look back. Defaults to 15."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := request.GetString("name", "")
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
				analyses[idx] = analyseService(svcName, tiltClient, sinceMinutes)
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
