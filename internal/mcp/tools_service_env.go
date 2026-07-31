package mcp

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/workspace"
)

// registerServiceEnvTool registers the service_env MCP tool (local environments only).
func registerServiceEnvTool(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("service_env",
		mcp.WithDescription(
			"Inspect and manage environment variables across local services, by reading and writing the config FILES in a service's checkout — "+
				"not the running process (a write takes effect on the next generate + restart). "+
				"Its 'stack' parameter therefore picks which CHECKOUT to read/write, where every other tool's 'stack' picks which running instances to act on. "+
				"Supports five actions: "+
				"'get' — show the env a service actually resolves to, with the rung each value comes from; "+
				"'diff' — compare resolved env across multiple services or a group side-by-side; "+
				"'set' — write a key=value to ONE service's manifest (env.values) or .envrc, and report if a higher rung overrides it "+
				"(to instead set a config-var for every service pointed at a named environment, use env_set); "+
				"'check' — audit resolved env for placeholder values (empty, or containing TODO/CHANGEME/<replace>/your-/example.com) "+
				"and asymmetry — a key set for some of the selected services but missing from the others; "+
				"'drift' — compare the resolved env against what the service's own repo declares it needs: the files its "+
				"devstack.service.yaml lists under config.sources (its deployment manifest / appsettings), "+
				"reporting declared keys that are unset locally, secret-backed keys with no local value, and keys set to a different value. "+
				"Use 'drift' before trusting a local run of a code path that reads config: a declared key that is unset locally does not error, "+
				"it silently falls back to the code's default, so the service runs a different configuration than the one it is standing in for. "+
				"A rung is one layer of the env precedence ladder, lowest first — .envrc, workspace env.files, service env.files, "+
				"workspace env.values, service env.values, active env (workspace, then service, then stack), devstack-computed — "+
				"and a higher rung overrides every rung below it. "+
				"'get' resolves every rung — reach for it to answer 'what is KEY set to'; "+
				"reach for env_which instead for the narrower question of which named config-patch env applies at each scope and what that patch alone contributes.",
		),
		mcp.WithString("action",
			mcp.Required(),
			mcp.Description("One of: get, diff, check, drift — read only, change nothing; set — writes a file (the service's devstack.service.yaml or its .envrc)."),
		),
		mcp.WithString("service",
			mcp.Description("Exact service name. For diff, may be comma-separated list of 2+ services."),
		),
		mcp.WithString("group",
			mcp.Description("Group name — a named set of services declared under 'groups' in the workspace manifest; expands to its member services. The environment tool lists this workspace's group names."),
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
		mcp.WithString("target",
			mcp.Description(
				"Where to write (required for set). "+
					"'manifest' — the service's devstack.service.yaml env.values. devstack treats that file as machine-local and expects it gitignored (it holds absolute tool paths), but whether it is ignored is per-repo — check with git check-ignore -v devstack.service.yaml before trusting it. "+
					"Use for devstack-managed config: service addresses, ports, URLs of other services, feature flags. "+
					"'envrc' — the service's local .envrc. Not committed. "+
					"Use for secrets and anything credential-bearing: API keys, tokens, passwords, DSNs with credentials. "+
					"Keep secrets out of 'manifest' regardless: .envrc is the conventional credential store, is never committed, and is what direnv loads.",
			),
		),
		mcp.WithString("stack",
			mcp.Description(
				"Optional. "+stackShortNameDesc+" "+
					"Source-tree semantics, unlike the running-process semantics 'stack' has on every other tool: it names the checkout to read/write. "+
					"Absent (default) reads/writes the BASE workspace's service repos, unchanged. "+
					"When set, reads/writes the named stack's worktree of the service instead of base — so an agent edits its "+
					"stack's config, never base's.",
			),
		),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		action := req.GetString("action", "")
		serviceName := req.GetString("service", "")
		groupName := req.GetString("group", "")
		filter := req.GetString("filter", "")
		key := req.GetString("key", "")
		value := req.GetString("value", "")
		target := req.GetString("target", "")
		stackName := req.GetString("stack", "")

		targetPath, instance, stackEnv, err := serviceEnvTarget(ws, workspacePath, stackName)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		cfg, _ := config.Load(targetPath)
		if cfg == nil {
			cfg = &config.WorkspaceConfig{
				Deps:         map[string][]string{},
				Groups:       map[string][]string{},
				ServicePaths: map[string]string{},
			}
		}

		switch action {
		case "get":
			res, err := handleServiceEnvGet(ws, targetPath, stackEnv, cfg, serviceName, groupName, filter)
			return prependInstanceResult(res, instance), err
		case "diff":
			res, err := handleServiceEnvDiff(ws, targetPath, stackEnv, cfg, serviceName, groupName, filter)
			return prependInstanceResult(res, instance), err
		case "set":
			res, err := handleServiceEnvSet(ws, targetPath, stackEnv, serviceName, key, value, target)
			return prependInstanceResult(res, instance), err
		case "check":
			res, err := handleServiceEnvCheck(ws, targetPath, stackEnv, cfg, serviceName, groupName)
			return prependInstanceResult(res, instance), err
		case "drift":
			res, err := handleServiceEnvDrift(ws, targetPath, stackEnv, cfg, serviceName, groupName)
			return prependInstanceResult(res, instance), err
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — must be one of: get, diff, set, check, drift", action)), nil
		}
	})
}

// resolveLadders resolves each service's env precedence ladder from the
// workspace's manifests — the same ladder the generator feeds the service, so
// what this reports is what the service gets. Services with no service manifest
// are absent from the result.
func resolveLadders(ws *workspace.Workspace, workspacePath, stackEnv string, services []string) (map[string][]config.EnvLayer, error) {
	rw, err := config.ResolveWorkspace(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace manifests: %w", err)
	}
	managed := workspace.ManagedEnv(ws, services, workspace.ActiveEnvNames(rw, stackEnv))

	out := map[string][]config.EnvLayer{}
	for _, name := range services {
		svc, ok := rw.Services[name]
		if !ok || svc.Manifest == nil {
			continue
		}
		layers, err := config.EnvLadder(svc.EnvDir(), rw.Manifest, svc.Manifest, stackEnv, managed[name])
		if err != nil {
			return nil, fmt.Errorf("failed to resolve env for %s: %w", name, err)
		}
		out[name] = layers
	}
	return out, nil
}

// resolvedEnvs flattens each service's ladder into the env it actually receives.
func resolvedEnvs(ws *workspace.Workspace, workspacePath, stackEnv string, services []string) (map[string]map[string]string, error) {
	ladders, err := resolveLadders(ws, workspacePath, stackEnv, services)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string, len(ladders))
	for name, layers := range ladders {
		out[name] = config.MergeEnvLadder(layers)
	}
	return out, nil
}

// describeRung names a rung for a human, without ever naming its value.
func describeRung(l config.EnvLayer) string {
	if l.Source == "" {
		return string(l.Rung)
	}
	return fmt.Sprintf("%s (%s)", l.Rung, l.Source)
}

// rungOf reports which rung supplies key, for display alongside a value.
func rungOf(layers []config.EnvLayer, key string) string {
	var winner config.EnvLayer
	found := false
	for _, l := range layers {
		if _, ok := l.Values[key]; ok {
			winner = l
			found = true
		}
	}
	if !found {
		return ""
	}
	return describeRung(winner)
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
func handleServiceEnvGet(ws *workspace.Workspace, workspacePath, stackEnv string, cfg *config.WorkspaceConfig, serviceName, groupName, filter string) (*mcp.CallToolResult, error) {
	services, err := resolveServices(cfg, serviceName, groupName)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	ladders, err := resolveLadders(ws, workspacePath, stackEnv, services)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var sb strings.Builder
	filterLower := strings.ToLower(filter)

	for _, svc := range services {
		fmt.Fprintf(&sb, "service: %s\n", svc)

		layers, ok := ladders[svc]
		if !ok {
			fmt.Fprintf(&sb, "  (no %s for %s \u2014 env cannot be resolved)\n\n", config.ServiceManifestFileName, svc)
			continue
		}

		env := config.MergeEnvLadder(layers)
		for _, k := range sortedKeys(env) {
			if filter != "" && !strings.Contains(strings.ToLower(k), filterLower) {
				continue
			}
			fmt.Fprintf(&sb, "  %s=%s  [%s]\n", k, env[k], rungOf(layers, k))
		}
		sb.WriteString("\n")
	}

	return mcp.NewToolResultText(sb.String()), nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// handleServiceEnvDiff implements the "diff" action.
func handleServiceEnvDiff(ws *workspace.Workspace, workspacePath, stackEnv string, cfg *config.WorkspaceConfig, serviceName, groupName, filter string) (*mcp.CallToolResult, error) {
	services, err := resolveServices(cfg, serviceName, groupName)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if len(services) < 2 {
		return mcp.NewToolResultError("diff requires at least 2 services (use group or comma-separated service list)"), nil
	}

	resolved, err := resolvedEnvs(ws, workspacePath, stackEnv, services)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	envMaps := make([]map[string]string, len(services))
	for i, svc := range services {
		if env, ok := resolved[svc]; ok {
			envMaps[i] = env
		} else {
			envMaps[i] = map[string]string{}
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

// handleServiceEnvSet implements the "set" action. It writes to the rung the
// caller names, then re-resolves the ladder from disk and reports whether the
// value can actually reach the service.
func handleServiceEnvSet(ws *workspace.Workspace, workspacePath, stackEnv, serviceName, key, value, target string) (*mcp.CallToolResult, error) {
	if serviceName == "" {
		return mcp.NewToolResultError("service is required for set"), nil
	}
	if key == "" {
		return mcp.NewToolResultError("key is required for set"), nil
	}

	var rung config.EnvRung
	switch target {
	case "manifest":
		rung = config.RungServiceValues
	case "envrc":
		rung = config.RungEnvrc
	case "":
		return mcp.NewToolResultError(
			"target is required for set — 'manifest' for devstack-managed config (env.values, committed to git), " +
				"'envrc' for secrets and credentials (local, not committed)"), nil
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown target %q — must be 'manifest' or 'envrc'", target)), nil
	}

	rw, err := config.ResolveWorkspace(workspacePath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to resolve workspace manifests: %v", err)), nil
	}
	svc, ok := rw.Services[serviceName]
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("service %q not found in workspace", serviceName)), nil
	}
	if svc.Manifest == nil {
		return mcp.NewToolResultError(fmt.Sprintf("service %q has no %s — env cannot be resolved for it", serviceName, config.ServiceManifestFileName)), nil
	}

	var written string
	if target == "manifest" {
		if err := config.SetServiceEnvValue(svc.RepoPath, key, value); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to write %s: %v", key, err)), nil
		}
		written = config.ServiceManifestPath(svc.RepoPath)
	} else {
		written, err = setEnvrcValue(svc.EnvDir(), key, value)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to write %s: %v", key, err)), nil
		}
	}

	layers, err := reresolveLadder(ws, workspacePath, stackEnv, serviceName)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf(
			"wrote %s to %s (%s), but the env ladder could not be resolved to confirm it takes effect: %v",
			key, written, rung, err)), nil
	}

	if over, overridden := config.OverriderOf(layers, rung, key); overridden {
		return mcp.NewToolResultText(fmt.Sprintf(
			"wrote %s to %s (%s) — but it will NOT reach %s: %s also sets %s and wins.\n"+
				"Set it at that rung instead, or remove it from there.",
			key, written, rung, serviceName, describeRung(over), key)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"wrote %s to %s (%s) — no higher rung overrides it. Takes effect for %s on its next restart: the restart tool "+
			"(CLI: devstack service restart %s) regenerates the Tiltfile from the manifests before triggering, so there is no separate generate step.",
		key, written, rung, serviceName, serviceName)), nil
}

// reresolveLadder re-reads the workspace from disk so the ladder reflects a write
// that just landed.
func reresolveLadder(ws *workspace.Workspace, workspacePath, stackEnv, serviceName string) ([]config.EnvLayer, error) {
	ladders, err := resolveLadders(ws, workspacePath, stackEnv, []string{serviceName})
	if err != nil {
		return nil, err
	}
	layers, ok := ladders[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q could not be resolved", serviceName)
	}
	return layers, nil
}

// setEnvrcValue updates or appends `export KEY='value'` in dir's .envrc and
// returns the path written. dir is the service's env dir, which is where the
// ladder reads .envrc from.
func setEnvrcValue(dir, key, value string) (string, error) {
	path := filepath.Join(dir, config.EnvrcFileName)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to read %s: %w", path, err)
	}

	lines := []string{}
	if len(data) > 0 {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("failed to parse %s: %w", path, err)
		}
	}

	// .envrc is executed, not line-parsed, so an unquoted value would be word-split.
	newLine := fmt.Sprintf("export %s=%s", key, shQuote(value))
	found := false
	for i, line := range lines {
		withoutExport := strings.TrimPrefix(strings.TrimSpace(line), "export ")
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

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", path, err)
	}
	return path, nil
}

// shQuote single-quotes a value for embedding in .envrc.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// placeholderPatterns are substrings that indicate a placeholder value.
var placeholderPatterns = []string{
	"TODO", "CHANGEME", "<replace>", "your-", "example.com",
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

type checkFinding struct {
	level   string // PASS, WARN, FAIL
	service string
	key     string
	message string
}

// handleServiceEnvCheck implements the "check" action. It audits the env each
// service actually resolves to. It deliberately makes no cross-service agreement
// claims: services agreeing is consensus, not correctness.
func handleServiceEnvCheck(ws *workspace.Workspace, workspacePath, stackEnv string, cfg *config.WorkspaceConfig, serviceName, groupName string) (*mcp.CallToolResult, error) {
	var services []string
	if groupName != "" || serviceName != "" {
		var err error
		services, err = resolveServices(cfg, serviceName, groupName)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	} else {
		for svc := range cfg.ServicePaths {
			services = append(services, svc)
		}
		sort.Strings(services)
	}

	svcEnvs, err := resolvedEnvs(ws, workspacePath, stackEnv, services)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var findings []checkFinding

	allKeys := map[string]bool{}
	for _, env := range svcEnvs {
		for k := range env {
			allKeys[k] = true
		}
	}

	for k := range allKeys {
		presentIn := []string{}
		for _, svc := range services {
			if _, ok := svcEnvs[svc][k]; ok {
				presentIn = append(presentIn, svc)
			}
		}

		if len(presentIn) > 0 && len(presentIn) < len(services) {
			for _, svc := range services {
				if _, ok := svcEnvs[svc][k]; !ok {
					findings = append(findings, checkFinding{
						level:   "WARN",
						service: svc,
						key:     k,
						message: fmt.Sprintf("missing (set in: %s)", strings.Join(presentIn, ", ")),
					})
				}
			}
		}

		for _, svc := range presentIn {
			if isPlaceholderValue(svcEnvs[svc][k]) {
				findings = append(findings, checkFinding{
					level:   "WARN",
					service: svc,
					key:     k,
					message: "placeholder or empty value",
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

// handleServiceEnvDrift implements the "drift" action: for each service, compare
// the env it actually resolves to against the config surface its own repo
// declares, and report every difference. With no service or group it covers
// every service that declares config sources.
func handleServiceEnvDrift(ws *workspace.Workspace, workspacePath, stackEnv string, cfg *config.WorkspaceConfig, serviceName, groupName string) (*mcp.CallToolResult, error) {
	var services []string
	if groupName != "" || serviceName != "" {
		var err error
		services, err = resolveServices(cfg, serviceName, groupName)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	} else {
		for svc := range cfg.ServicePaths {
			services = append(services, svc)
		}
		sort.Strings(services)
	}

	rw, err := config.ResolveWorkspace(workspacePath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to resolve workspace manifests: %v", err)), nil
	}
	svcEnvs, err := resolvedEnvs(ws, workspacePath, stackEnv, services)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var sb strings.Builder
	var undeclared []string
	total := 0
	for _, name := range services {
		svc, ok := rw.Services[name]
		if !ok || svc.Manifest == nil || len(svc.Manifest.Config.Sources) == 0 {
			undeclared = append(undeclared, name)
			continue
		}
		entries, err := svcconfig.Drift(svc, svcEnvs[name])
		if err != nil {
			fmt.Fprintf(&sb, "%s: could not compare — %v\n\n", name, err)
			continue
		}
		total += len(entries)
		sb.WriteString(svcconfig.Render(name, entries))
		sb.WriteString("\n")
	}

	if len(undeclared) > 0 {
		fmt.Fprintf(&sb, "not comparable (no config.sources declared in devstack.service.yaml): %s\n", strings.Join(undeclared, ", "))
		sb.WriteString("Point config.sources at the service's deployment manifest or appsettings file to make it comparable.\n")
	}
	if total > 0 {
		sb.WriteString("\n'missing' is the one to act on: the key is declared with a value where the service is deployed but unset here, so the local process silently uses its code default.\n")
		sb.WriteString("Fix by setting it with service_env action=set (target=manifest for plain config, target=envrc for anything credential-bearing), then restart the service.\n")
	}

	return mcp.NewToolResultText(sb.String()), nil
}
