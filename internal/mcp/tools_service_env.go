package mcp

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"devstack/internal/config"
	"devstack/internal/tilt"
	"devstack/internal/workspace"
)

// registerServiceEnvTool registers the service_env MCP tool (local environments only).
func registerServiceEnvTool(mcpServer *server.MCPServer, tiltClient *tilt.Client, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("service_env",
		mcp.WithDescription(
			"Inspect and manage environment variables across local services. "+
				"Supports four actions: "+
				"'get' — show configured (.envrc) and live (/proc/<pid>/environ) env vars for a service or group; "+
				"'diff' — compare .envrc env vars across multiple services or a group side-by-side; "+
				"'set' — update or append a key=value in a service's .envrc file; "+
				"'check' — audit services for missing, mismatched, or suspicious env var patterns (DB URLs, OTEL endpoints, placeholders).",
		),
		mcp.WithString("action",
			mcp.Required(),
			mcp.Description("One of: get, diff, set, check"),
		),
		mcp.WithString("service",
			mcp.Description("Exact service name. For diff, may be comma-separated list of 2+ services."),
		),
		mcp.WithString("group",
			mcp.Description("Group name — expands to member services."),
		),
		mcp.WithString("filter",
			mcp.Description("Substring filter on key names (case-insensitive). Applies to get and diff."),
		),
		mcp.WithString("key",
			mcp.Description("Env var key name (required for set)."),
		),
		mcp.WithString("value",
			mcp.Description("Env var value (required for set)."),
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		action := req.GetString("action", "")
		serviceName := req.GetString("service", "")
		groupName := req.GetString("group", "")
		filter := req.GetString("filter", "")
		key := req.GetString("key", "")
		value := req.GetString("value", "")

		cfg, _ := config.Load(workspacePath)
		if cfg == nil {
			cfg = &config.WorkspaceConfig{
				Deps:         map[string][]string{},
				Groups:       map[string][]string{},
				ServicePaths: map[string]string{},
			}
		}

		switch action {
		case "get":
			return handleServiceEnvGet(tiltClient, cfg, serviceName, groupName, filter)
		case "diff":
			return handleServiceEnvDiff(cfg, serviceName, groupName, filter)
		case "set":
			return handleServiceEnvSet(cfg, serviceName, key, value)
		case "check":
			return handleServiceEnvCheck(cfg, ws, serviceName, groupName)
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — must be one of: get, diff, set, check", action)), nil
		}
	})
}

// parseEnvrc reads a .envrc file and returns a map of KEY -> VALUE.
// Handles: export KEY=VALUE, KEY=VALUE; strips surrounding quotes; skips comments and blanks.
func parseEnvrc(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	result := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip leading "export "
		line = strings.TrimPrefix(line, "export ")
		idx := strings.IndexByte(line, '=')
		if idx < 1 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		// Strip surrounding quotes (single or double)
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		result[k] = v
	}
	return result, scanner.Err()
}

// readProcEnv reads /proc/<pid>/environ and returns a KEY -> VALUE map.
func readProcEnv(pid int) (map[string]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, fmt.Errorf("cannot read /proc/%d/environ: %w", pid, err)
	}
	result := map[string]string{}
	for _, entry := range strings.Split(string(data), "\x00") {
		if entry == "" {
			continue
		}
		idx := strings.IndexByte(entry, '=')
		if idx < 1 {
			continue
		}
		result[entry[:idx]] = entry[idx+1:]
	}
	return result, nil
}

// pidForService looks up the PID from Tilt for the given service. Returns 0 if not available.
func pidForService(tiltClient *tilt.Client, serviceName string) int {
	view, err := tiltClient.GetView()
	if err != nil {
		return 0
	}
	for _, r := range view.UiResources {
		if r.Metadata.Name == serviceName {
			if r.Status.RuntimeStatus != "ok" {
				return 0
			}
			// Tilt does not expose PID directly in the UIResource; live env is unavailable.
			return 0
		}
	}
	return 0
}

// resolveServices expands service/group inputs to a list of service names.
func resolveServices(cfg *config.WorkspaceConfig, serviceName, groupName string) ([]string, error) {
	if groupName != "" {
		members, ok := cfg.Groups[groupName]
		if !ok {
			return nil, fmt.Errorf("group %q not found. Available groups: %s", groupName, availableGroups(cfg))
		}
		return members, nil
	}
	if serviceName != "" {
		// Allow comma-separated list
		parts := strings.Split(serviceName, ",")
		var services []string
		for _, p := range parts {
			s := strings.TrimSpace(p)
			if s != "" {
				services = append(services, s)
			}
		}
		return services, nil
	}
	return nil, fmt.Errorf("specify either service or group")
}

// handleServiceEnvGet implements the "get" action.
func handleServiceEnvGet(tiltClient *tilt.Client, cfg *config.WorkspaceConfig, serviceName, groupName, filter string) (*mcp.CallToolResult, error) {
	services, err := resolveServices(cfg, serviceName, groupName)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var sb strings.Builder

	for _, svc := range services {
		fmt.Fprintf(&sb, "service: %s\n", svc)

		svcPath, ok := cfg.ServicePaths[svc]
		if !ok {
			fmt.Fprintf(&sb, "  (no service path configured for %s)\n\n", svc)
			continue
		}

		envrcPath := svcPath + "/.envrc"
		configured, err := parseEnvrc(envrcPath)
		if err != nil {
			fmt.Fprintf(&sb, "  error reading .envrc: %v\n\n", err)
			continue
		}

		// Try to get live process env
		pid := pidForService(tiltClient, svc)
		var live map[string]string
		var liveNote string
		if pid > 0 {
			live, err = readProcEnv(pid)
			if err != nil {
				liveNote = fmt.Sprintf(" (live env unavailable: %v)", err)
			}
		} else {
			liveNote = " (live env unavailable: PID not exposed by Tilt)"
		}

		if liveNote != "" {
			fmt.Fprintf(&sb, "  note:%s\n", liveNote)
		}

		// Collect all keys
		allKeys := map[string]bool{}
		for k := range configured {
			allKeys[k] = true
		}
		if live != nil {
			for k := range live {
				allKeys[k] = true
			}
		}

		keys := make([]string, 0, len(allKeys))
		for k := range allKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		filterLower := strings.ToLower(filter)
		for _, k := range keys {
			if filter != "" && !strings.Contains(strings.ToLower(k), filterLower) {
				continue
			}
			cfgVal, inCfg := configured[k]
			liveVal, inLive := live[k]

			var line string
			switch {
			case live == nil:
				// No live data
				line = fmt.Sprintf("  %s=%s", k, cfgVal)
			case inCfg && inLive && cfgVal == liveVal:
				line = fmt.Sprintf("  %s=%s", k, cfgVal)
			case inCfg && inLive && cfgVal != liveVal:
				line = fmt.Sprintf("  %s=%s\u2260live (%s)", k, cfgVal, liveVal)
			case inCfg && !inLive:
				line = fmt.Sprintf("  %s=%s  (configured only)", k, cfgVal)
			case !inCfg && inLive:
				line = fmt.Sprintf("  %s=%s  (live only)", k, liveVal)
			}
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	return mcp.NewToolResultText(sb.String()), nil
}

// handleServiceEnvDiff implements the "diff" action.
func handleServiceEnvDiff(cfg *config.WorkspaceConfig, serviceName, groupName, filter string) (*mcp.CallToolResult, error) {
	services, err := resolveServices(cfg, serviceName, groupName)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if len(services) < 2 {
		return mcp.NewToolResultError("diff requires at least 2 services (use group or comma-separated service list)"), nil
	}

	// Collect env from each service
	envMaps := make([]map[string]string, len(services))
	for i, svc := range services {
		svcPath, ok := cfg.ServicePaths[svc]
		if !ok {
			envMaps[i] = map[string]string{}
			continue
		}
		envMaps[i], err = parseEnvrc(svcPath + "/.envrc")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("error reading .envrc for %s: %v", svc, err)), nil
		}
	}

	// Collect keys that appear in at least 2 services
	keyCount := map[string]int{}
	for _, m := range envMaps {
		for k := range m {
			keyCount[k]++
		}
	}

	filterLower := strings.ToLower(filter)
	sharedKeys := []string{}
	for k, count := range keyCount {
		if count < 2 {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(k), filterLower) {
			continue
		}
		sharedKeys = append(sharedKeys, k)
	}
	sort.Strings(sharedKeys)

	if len(sharedKeys) == 0 {
		return mcp.NewToolResultText("No shared keys found across selected services."), nil
	}

	var sb strings.Builder

	// Column widths
	keyWidth := 28
	valWidth := 24

	// Header
	fmt.Fprintf(&sb, "%-10s %-*s", "STATUS", keyWidth, "KEY")
	for _, svc := range services {
		if len(svc) > valWidth {
			svc = svc[:valWidth-1] + "…"
		}
		fmt.Fprintf(&sb, " %-*s", valWidth, svc)
	}
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("-", 10+1+keyWidth+len(services)*(valWidth+1)) + "\n")

	for _, k := range sharedKeys {
		vals := make([]string, len(services))
		mismatch := false
		first := ""
		for i, m := range envMaps {
			v := m[k]
			vals[i] = v
			if i == 0 {
				first = v
			} else if v != first {
				mismatch = true
			}
		}

		prefix := "OK     "
		if mismatch {
			prefix = "MISMATCH"
		}

		key := k
		if len(key) > keyWidth {
			key = key[:keyWidth-1] + "…"
		}
		fmt.Fprintf(&sb, "%-10s %-*s", prefix, keyWidth, key)
		for _, v := range vals {
			if len(v) > valWidth {
				v = v[:valWidth-1] + "…"
			}
			fmt.Fprintf(&sb, " %-*s", valWidth, v)
		}
		sb.WriteString("\n")
	}

	return mcp.NewToolResultText(sb.String()), nil
}

// handleServiceEnvSet implements the "set" action.
func handleServiceEnvSet(cfg *config.WorkspaceConfig, serviceName, key, value string) (*mcp.CallToolResult, error) {
	if serviceName == "" {
		return mcp.NewToolResultError("service is required for set"), nil
	}
	if key == "" {
		return mcp.NewToolResultError("key is required for set"), nil
	}

	svcPath, ok := cfg.ServicePaths[serviceName]
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("service %q not found in service_paths config", serviceName)), nil
	}

	envrcPath := svcPath + "/.envrc"
	data, err := os.ReadFile(envrcPath)
	if err != nil && !os.IsNotExist(err) {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read %s: %v", envrcPath, err)), nil
	}

	lines := []string{}
	if len(data) > 0 {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to parse %s: %v", envrcPath, err)), nil
		}
	}

	newLine := fmt.Sprintf("export %s=%s", key, value)
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match "export KEY=..." or "KEY=..."
		withoutExport := strings.TrimPrefix(trimmed, "export ")
		if strings.HasPrefix(withoutExport, key+"=") {
			lines[i] = newLine
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, newLine)
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	if err := os.WriteFile(envrcPath, []byte(content), 0644); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write %s: %v", envrcPath, err)), nil
	}

	action := "updated"
	if !found {
		action = "appended"
	}
	return mcp.NewToolResultText(fmt.Sprintf("%s: %s in %s", action, newLine, envrcPath)), nil
}

// dbURLPatterns are key name patterns that indicate database connection strings.
var dbURLPatterns = []string{
	"_DATABASE_URL", "_DB_URL", "_DSN", "_POSTGRES_URL", "_MYSQL_URL", "_MONGO_URL", "_REDIS_URL",
}

// otelEndpointKeys are OTEL exporter endpoint keys to audit.
var otelEndpointKeys = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
}

// placeholderPatterns are substrings that indicate a placeholder value.
var placeholderPatterns = []string{
	"TODO", "CHANGEME", "<replace>", "your-", "example.com",
}

// isDBURLKey returns true if the key name matches a database URL pattern.
func isDBURLKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, pat := range dbURLPatterns {
		if strings.HasSuffix(upper, pat) {
			return true
		}
	}
	return false
}

// isOtelEndpointKey returns true if the key is an OTEL endpoint key.
func isOtelEndpointKey(key string) bool {
	for _, k := range otelEndpointKeys {
		if key == k {
			return true
		}
	}
	return false
}

// isPlaceholderValue returns true if the value looks like a placeholder.
func isPlaceholderValue(v string) bool {
	if v == "" {
		return true
	}
	upper := strings.ToUpper(v)
	for _, pat := range placeholderPatterns {
		if strings.Contains(upper, strings.ToUpper(pat)) {
			return true
		}
	}
	return false
}

// isSafeHost returns true if the host is considered local/docker-safe.
func isSafeHost(host string) bool {
	if host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" {
		return true
	}
	// Docker gateway IPs
	if host == "172.17.0.1" || host == "host-gateway" {
		return true
	}
	// No dots => likely a docker service name
	if !strings.Contains(host, ".") {
		return true
	}
	return false
}

// extractHost parses a host from a DSN or URL value.
func extractHost(val string) string {
	// Try URL parse first
	u, err := url.Parse(val)
	if err == nil && u.Host != "" {
		h := u.Hostname()
		return h
	}
	// Fallback: look for @host or first host-like token
	return ""
}

type checkFinding struct {
	level   string // PASS, WARN, FAIL, MISMATCH
	service string
	key     string
	message string
}

// handleServiceEnvCheck implements the "check" action.
func handleServiceEnvCheck(cfg *config.WorkspaceConfig, ws *workspace.Workspace, serviceName, groupName string) (*mcp.CallToolResult, error) {
	// Determine services to check
	var services []string
	if groupName != "" || serviceName != "" {
		var err error
		services, err = resolveServices(cfg, serviceName, groupName)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	} else {
		// All registered services
		for svc := range cfg.ServicePaths {
			services = append(services, svc)
		}
		sort.Strings(services)
	}

	// Load env for each service
	svcEnvs := make(map[string]map[string]string, len(services))
	for _, svc := range services {
		svcPath, ok := cfg.ServicePaths[svc]
		if !ok {
			svcEnvs[svc] = map[string]string{}
			continue
		}
		env, err := parseEnvrc(svcPath + "/.envrc")
		if err != nil {
			svcEnvs[svc] = map[string]string{}
			continue
		}
		svcEnvs[svc] = env
	}

	var findings []checkFinding

	// Expected OTEL endpoints
	expectedHTTP := fmt.Sprintf("http://localhost:%d", ws.HTTPPort())
	expectedGRPC := fmt.Sprintf("http://localhost:%d", ws.GRPCPort())

	// Collect all unique keys across services
	allKeys := map[string]bool{}
	for _, env := range svcEnvs {
		for k := range env {
			allKeys[k] = true
		}
	}

	// Per-key analysis
	for k := range allKeys {
		// Gather which services have this key and what their values are
		svcValues := map[string]string{}
		for _, svc := range services {
			if v, ok := svcEnvs[svc][k]; ok {
				svcValues[svc] = v
			}
		}

		// OTEL endpoint checks
		if isOtelEndpointKey(k) {
			for _, svc := range services {
				v, present := svcEnvs[svc][k]
				if !present || v == "" {
					findings = append(findings, checkFinding{
						level:   "FAIL",
						service: svc,
						key:     k,
						message: "missing or empty",
					})
					continue
				}
				if v != expectedHTTP && v != expectedGRPC {
					// Check if it's right host but wrong port
					u, err := url.Parse(v)
					if err == nil && u.Hostname() == "localhost" {
						findings = append(findings, checkFinding{
							level:   "WARN",
							service: svc,
							key:     k,
							message: fmt.Sprintf("%s (expected %s or %s)", v, expectedHTTP, expectedGRPC),
						})
					} else {
						findings = append(findings, checkFinding{
							level:   "FAIL",
							service: svc,
							key:     k,
							message: fmt.Sprintf("%s (expected %s or %s)", v, expectedHTTP, expectedGRPC),
						})
					}
				} else {
					findings = append(findings, checkFinding{
						level:   "PASS",
						service: svc,
						key:     k,
						message: v,
					})
				}
			}
			continue
		}

		// DB URL checks
		if isDBURLKey(k) {
			// Check for presence mismatch: present in some but not others
			presentIn := []string{}
			missingIn := []string{}
			for _, svc := range services {
				if _, ok := svcEnvs[svc][k]; ok {
					presentIn = append(presentIn, svc)
				} else {
					missingIn = append(missingIn, svc)
				}
			}
			if len(presentIn) > 0 && len(missingIn) > 0 {
				for _, svc := range missingIn {
					findings = append(findings, checkFinding{
						level:   "FAIL",
						service: svc,
						key:     k,
						message: fmt.Sprintf("missing (present in: %s)", strings.Join(presentIn, ", ")),
					})
				}
			}

			// Check for value drift among services that have it
			if len(presentIn) > 1 {
				firstSvc := presentIn[0]
				firstVal := svcValues[firstSvc]
				for _, svc := range presentIn[1:] {
					v := svcValues[svc]
					if v != firstVal {
						findings = append(findings, checkFinding{
							level:   "MISMATCH",
							service: fmt.Sprintf("%s vs %s", firstSvc, svc),
							key:     k,
							message: fmt.Sprintf("%s \u2260 %s", firstVal, v),
						})
					}
				}
			}

			// Check for non-local hosts
			for _, svc := range presentIn {
				v := svcValues[svc]
				host := extractHost(v)
				if host != "" && !isSafeHost(host) {
					findings = append(findings, checkFinding{
						level:   "WARN",
						service: svc,
						key:     k,
						message: fmt.Sprintf("%s (host %q may be remote/prod)", v, host),
					})
				} else if host != "" {
					findings = append(findings, checkFinding{
						level:   "PASS",
						service: svc,
						key:     k,
						message: v,
					})
				}
			}
			continue
		}

		// General: asymmetric config (key in some but not all services)
		if len(svcValues) > 0 && len(svcValues) < len(services) {
			for _, svc := range services {
				if _, ok := svcEnvs[svc][k]; !ok {
					presentIn := []string{}
					for _, s := range services {
						if _, ok2 := svcEnvs[s][k]; ok2 {
							presentIn = append(presentIn, s)
						}
					}
					findings = append(findings, checkFinding{
						level:   "WARN",
						service: svc,
						key:     k,
						message: fmt.Sprintf("missing (set in: %s)", strings.Join(presentIn, ", ")),
					})
				}
			}
		}

		// General: placeholder values
		for _, svc := range services {
			v, ok := svcEnvs[svc][k]
			if !ok {
				continue
			}
			if isPlaceholderValue(v) {
				findings = append(findings, checkFinding{
					level:   "WARN",
					service: svc,
					key:     k,
					message: fmt.Sprintf("placeholder value: %q", v),
				})
			}
		}
	}

	// Sort findings for stable output
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].level != findings[j].level {
			return findings[i].level < findings[j].level
		}
		if findings[i].key != findings[j].key {
			return findings[i].key < findings[j].key
		}
		return findings[i].service < findings[j].service
	})

	var sb strings.Builder

	failures := 0
	warnings := 0
	passes := 0

	for _, f := range findings {
		switch f.level {
		case "FAIL":
			failures++
		case "WARN":
			warnings++
		case "PASS":
			passes++
		}
		fmt.Fprintf(&sb, "%-10s %-30s %-40s %s\n", f.level, f.service, f.key, f.message)
	}

	if len(findings) == 0 {
		sb.WriteString("No issues found.\n")
	}

	fmt.Fprintf(&sb, "\nSummary: %d failure(s), %d warning(s), %d passed\n", failures, warnings, passes)

	return mcp.NewToolResultText(sb.String()), nil
}
