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
			"Show the active workspace and the available tools. Call this tool first, to learn what you can do and can not do in this context. "+
				baseTermDesc+
				"A tool that starts, stops or restarts a service must be told which copy to act on: a stack's short name, or \"base\". The read-only tools default to base. "+
				"An 'env' here is a CONFIG-PATCH environment. That is a named set of config vars, for example 'staging'. env_use points a workspace, a service or a stack at one of them (CLI: devstack env use). status and env_which show which env each copy points at. "+
				"devstack is a LOCAL development environment. Its data is local and ephemeral, and it is not production. "+
				"The tools available depend on this workspace's configuration. The investigate tool appears only where this workspace has observability. The observability tool is always there, and it is the tool that enables observability. The tunnel tool appears only when this machine has an ssh client.",
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
		sb.WriteString("copies: base runs from a replica of the workspace, and never from the user's checkouts. Those checkouts are the template devstack builds the replica from, and nothing runs in them.\n" +
			"  start, stop, restart and env_use take the copy from stack=\"<name>\" or stack=\"base\". With neither, devstack reads the copy from the working directory, and uses base where that directory is neither a stack nor the replica. The read-only tools default to base too.\n" +
			"  The base tool works on the replica. action=\"path\" prints where base runs from. action=\"sync\" moves the replica to each service's default branch tip, and it restarts nothing. An edit in a checkout reaches a running base copy only after it is on the default branch, that sync has run, and the copy has restarted (restart tool, stack=\"base\"). To see a change run now, put it in a stack.\n")
		if line := servicesSummary(workspacePath, defaultService); line != "" {
			sb.WriteString(line)
		}
		tools := []string{"status", "start", "restart", "stop", "topology", "configure", "process_logs", "service_env", "base", "observability", "hooks", "migrate", "stack_create", "stack_add", "stack_list", "stack_note", "stack_up", "stack_down", "stack_rm", "env_use", "env_which", "env_set"}
		if otelOn {
			tools = append(tools, "investigate")
		}
		if tunnelsOn {
			tools = append(tools, "tunnel")
		}
		fmt.Fprintf(&sb, "tools: %s\n", strings.Join(tools, ", "))
		if otelOn {
			fmt.Fprintf(&sb, "recommended order: environment -> status -> observability -> process_logs/investigate\n")
			fmt.Fprintf(&sb, "query scope: telemetry results stay inside this workspace. investigate's recent-executions mode defaults to base and to this repo's service. To widen it, pass a stack's short name, or 'all'. Its attribute search has no default service. A trace_id/span_id lookup ignores service and stack entirely.\n")
		} else {
			fmt.Fprintf(&sb, "recommended order: environment -> status -> process_logs\n")
			fmt.Fprintf(&sb, "note: observability disabled — the investigate tool is not registered. Turn observability on with the observability tool (action=config_on).\n")
		}
		if !tunnelsOn {
			fmt.Fprintf(&sb, "note: tunnel tool unavailable — no ssh client on this machine.\n")
		}
		sb.WriteString(hooksSummary(ws))

		if cfg, err := config.Load(workspacePath); err == nil && len(cfg.Groups) > 0 {
			fmt.Fprintf(&sb, "groups: %s\n", availableGroups(cfg))
		}
		fmt.Fprintf(&sb, "envs: %s\n", envCatalog(workspacePath))
		fmt.Fprintf(&sb, "config-patch env: env_use points a service, the workspace or a stack at a named config env (devstack env use). status and env_which show where each copy points.\n")

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
	return fmt.Sprintf("services: %s%s%s — exact names. Other tools reject a partial match\n", strings.Join(listed, ", "), suffix, def)
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
		line += "down = the stack's worktrees and its record exist, and none of its services run. status, process_logs, restart, stop and configure against it " +
			"error \"not up\" instead of falling through to base. " +
			"service_env still reads and writes its worktree config. investigate returns only what the stack emitted while it was last up.\n"
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
	return fmt.Sprintf("hooks: %d declared. They fire on %s\n"+
		"  They run on their own when you call stack_create, stack_add, stack_up, stack_down, stack_rm, start or stop. They can change state outside this machine. "+
		"List them with the hooks tool before you create or tear down a stack. A SETUP hook that fails means that the thing exists and is not provisioned. A TEARDOWN hook that fails means that the teardown finished and the external cleanup probably did not.\n",
		total, strings.Join(parts, ", "))
}
