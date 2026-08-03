package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hooks"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// registerEnvironmentTool registers the "environment" tool which orients agents immediately.
// This is the MOST IMPORTANT discoverability tool — calling it first reveals what's possible.
func registerEnvironmentTool(mcpServer *server.MCPServer, obsURL, workspaceName, workspacePath, defaultService string, ws *workspace.Workspace) {
	tool := mcp.NewTool("environment",
		mcp.WithDescription(
			"Show the active workspace and available tools. "+
				"Call this first to understand what you can and cannot do in the current context. "+
				baseTermDesc+
				"A tool that starts, stops or restarts a service has to be told which copy to act on — a stack's short name, or \"base\". The read-only tools default to base. "+
				"An 'env' here is a CONFIG-PATCH environment — a named set of config vars (for example 'staging') that a workspace, service, or stack instance is pointed at via env_use (CLI: devstack env use). status and env_which show which env each instance currently points at. "+
				"devstack is a LOCAL development environment. Data is ephemeral and local — not production. "+
				"The available tools depend on this workspace's configuration: trace/telemetry tools appear only when observability is enabled, tunnel tools only when an ssh client is available. "+
				"Call this tool first to understand the context before using other tools.",
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var sb strings.Builder

		otelOn := config.ObservabilityEnabled(workspacePath)
		tunnelsOn := coreSSHAvailable()

		backend := "none"
		if otelOn {
			backend = otel.For(ws).Name()
		}
		if otelOn {
			fmt.Fprintf(&sb, "observability: %s@%s + local dev daemon — local dev only, ephemeral data\n", backend, obsURL)
		} else {
			fmt.Fprintf(&sb, "observability: local dev daemon (disabled) — local dev only, ephemeral data\n")
		}

		if line := stacksSummary(ws); line != "" {
			sb.WriteString(line)
		}
		sb.WriteString("instances: base runs from a replica of the workspace (CLI: devstack base path), never from the user's checkouts — those are the template it is built from, and nothing runs in them.\n" +
			"  start, stop, restart and env_use have no default instance: pass stack=\"<name>\" or stack=\"base\", or the call is read from the working directory and fails where that is neither. The read-only tools still default to base.\n")
		if line := servicesSummary(workspacePath, defaultService); line != "" {
			sb.WriteString(line)
		}
		tools := []string{"status", "start", "restart", "stop", "topology", "configure", "process_logs", "service_env", "observability", "hooks", "stack_create", "stack_list", "stack_note", "stack_up", "stack_down", "stack_rm", "env_use", "env_which", "env_set"}
		if otelOn {
			tools = append(tools, "investigate")
		}
		if tunnelsOn {
			tools = append(tools, "tunnel")
		}
		fmt.Fprintf(&sb, "tools: %s\n", strings.Join(tools, ", "))
		if otelOn {
			fmt.Fprintf(&sb, "recommended order: environment -> status -> observability -> process_logs/investigate\n")
			fmt.Fprintf(&sb, "query scope: telemetry results stay inside this workspace; investigate's recent-executions mode defaults to the base instance and to this repo's service (pass a stack's short name, or 'all'), its attribute search has no default service, and a trace_id/span_id lookup ignores service and stack entirely.\n")
		} else {
			fmt.Fprintf(&sb, "recommended order: environment -> status -> process_logs\n")
			fmt.Fprintf(&sb, "note: observability disabled — no trace/telemetry tools. Turn it on with the observability tool (action=config_on).\n")
		}
		if !tunnelsOn {
			fmt.Fprintf(&sb, "note: tunnel tool unavailable — no ssh client on this machine.\n")
		}
		sb.WriteString(hooksSummary(ws))

		if cfg, err := config.Load(workspacePath); err == nil && len(cfg.Groups) > 0 {
			fmt.Fprintf(&sb, "groups: %s\n", availableGroups(cfg))
		}
		fmt.Fprintf(&sb, "envs: %s\n", envCatalog(workspacePath))
		fmt.Fprintf(&sb, "config-patch env: services/workspace/stack are pointed at a named config env via env_use (devstack env use); status and env_which show where each instance points.\n")

		return mcp.NewToolResultText(sb.String()), nil
	})
}

// envCatalog lists the config-patch environments the workspace manifest defines.
func envCatalog(workspacePath string) string {
	m, err := config.LoadWorkspaceManifest(workspacePath)
	if err != nil || len(m.Environments) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(m.Environments))
	for name := range m.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// servicesSummary names the workspace's services, the exact vocabulary every
// other tool's 'service' parameter demands, plus the default one this server
// falls back to. Long service sets are capped so orientation stays cheap.
func servicesSummary(workspacePath, defaultService string) string {
	cfg, err := config.Load(workspacePath)
	if err != nil || cfg == nil || len(cfg.ServicePaths) == 0 {
		return ""
	}
	names := make([]string, 0, len(cfg.ServicePaths))
	for name := range cfg.ServicePaths {
		names = append(names, name)
	}
	sort.Strings(names)

	listed := names
	suffix := ""
	if len(names) > servicesListCap {
		listed = names[:servicesListCap]
		suffix = fmt.Sprintf(" ... and %d more — devstack status lists all", len(names)-servicesListCap)
	}

	def := ""
	if defaultService != "" {
		def = fmt.Sprintf(" (default: %s)", defaultService)
	}
	return fmt.Sprintf("services: %s%s%s — exact names; other tools reject partial matches\n", strings.Join(listed, ", "), suffix, def)
}

// servicesListCap bounds how many service names orientation prints inline.
const servicesListCap = 12

// stacksSummary reports the workspace identity and its in-flight feature stacks,
// so an agent bound to the base immediately sees the other versions in flight.
func stacksSummary(ws *workspace.Workspace) string {
	if ws == nil {
		return ""
	}
	stacks, err := stack.List(ws.Name)
	if err != nil {
		return ""
	}
	if len(stacks) == 0 {
		return fmt.Sprintf("workspace: %s — base only, no feature stacks in flight\n", ws.Name)
	}
	parts := make([]string, 0, len(stacks))
	anyDown := false
	for _, s := range stacks {
		short := strings.TrimPrefix(s.Name, s.BaseName+"--")
		if s.Status == stack.StatusUp {
			parts = append(parts, fmt.Sprintf("%s (%s, base :%d)", short, stack.StatusUp, s.BasePort))
		} else {
			anyDown = true
			parts = append(parts, fmt.Sprintf("%s (%s)", short, s.Status))
		}
	}
	line := fmt.Sprintf("workspace: %s — base + %d feature stack(s) in flight: %s — those are the short names every 'stack' parameter takes (full identity is %s--<name>)\n", ws.Name, len(stacks), strings.Join(parts, ", "), ws.Name)
	if anyDown {
		line += "down = the stack's worktrees and record exist but none of its services run: status/process_logs/restart/stop/configure against it error \"not up\" instead of falling through to base, " +
			"service_env still reads and writes its worktree config, and investigate returns only what it emitted while it was last up.\n"
	}
	return line
}

// hooksSummary orients an agent on lifecycle hooks before it creates or destroys
// anything. Creating a stack in a workspace with hooks can run someone's shell
// command against external state, so whether any exist belongs in the first tool
// an agent calls, not in a tool it can never reach.
func hooksSummary(ws *workspace.Workspace) string {
	if ws == nil {
		return ""
	}
	events := map[string][]string{}
	total := 0
	for _, event := range config.HookEvents() {
		ev, src, err := hooks.Context(ws, "", event, nil)
		if err != nil {
			return ""
		}
		for _, inv := range hooks.Resolve(ev, src) {
			events[event] = append(events[event], inv.Label())
			total++
		}
	}
	if total == 0 {
		return "hooks: none declared — no lifecycle automation runs in this workspace\n"
	}

	var parts []string
	for _, event := range config.HookEvents() {
		if names := events[event]; len(names) > 0 {
			parts = append(parts, fmt.Sprintf("%s (%s)", event, strings.Join(names, ", ")))
		}
	}
	return fmt.Sprintf("hooks: %d declared, firing on %s\n"+
		"  These run automatically on stack_create/stack_up/stack_down/stack_rm/start/stop and can change state outside this machine. "+
		"List them with the hooks tool before creating or tearing down a stack. A failed SETUP hook means the thing exists but is not provisioned; a failed TEARDOWN hook means teardown finished but external cleanup probably did not.\n",
		total, strings.Join(parts, ", "))
}
