package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// registerEnvironmentTool registers the "environment" tool which orients agents immediately.
// This is the MOST IMPORTANT discoverability tool — calling it first reveals what's possible.
func registerEnvironmentTool(mcpServer *server.MCPServer, activeEnvName string,
	activeEnv workspace.Environment, allEnvs map[string]workspace.Environment, workspaceName, workspacePath string, ws *workspace.Workspace) {

	tool := mcp.NewTool("environment",
		mcp.WithDescription(
			"Show the active environment and available tools. "+
				"Call this first to understand what you can and cannot do in the current context. "+
				"Environments: local (full control) vs remote (observability-only, no service restart/stop). "+
				"devstack is a LOCAL development environment. Data is ephemeral and local — not production. "+
				"The available tools depend on this workspace's configuration: trace/telemetry tools appear only when observability is enabled, tunnel tools only when tailscale is installed. "+
				"Call this tool first to understand the context before using other tools.",
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var sb strings.Builder

		otelOn := activeEnv.Type != workspace.EnvironmentTypeLocal || config.ObservabilityEnabled(workspacePath)
		tunnelsOn := tailscaleInstalled()

		backend := activeEnv.Observability.Backend
		if backend == "" {
			backend = "signoz"
		}
		if !otelOn {
			backend = "none"
		}
		if activeEnv.Observability.OTLPEndpoint != "" {
			fmt.Fprintf(&sb, "env: %s (%s) %s@%s  otlp->%s\n", activeEnvName, activeEnv.Type, backend, activeEnv.Observability.URL, activeEnv.Observability.OTLPEndpoint)
		} else {
			fmt.Fprintf(&sb, "env: %s (%s) %s@%s\n", activeEnvName, activeEnv.Type, backend, activeEnv.Observability.URL)
		}
		if otelOn {
			fmt.Fprintf(&sb, "observability: %s + local dev daemon — local dev only, ephemeral data\n", backend)
		} else {
			fmt.Fprintf(&sb, "observability: local dev daemon (disabled) — local dev only, ephemeral data\n")
		}

		if activeEnv.Type == workspace.EnvironmentTypeLocal {
			if line := stacksSummary(ws); line != "" {
				sb.WriteString(line)
			}
			tools := []string{"status", "restart", "stop", "configure", "process_logs", "service_env", "observability", "stack_create", "stack_list", "stack_rm"}
			if otelOn {
				tools = append(tools, "investigate")
			}
			if tunnelsOn {
				tools = append(tools, "tunnel")
			}
			fmt.Fprintf(&sb, "tools: %s\n", strings.Join(tools, ", "))
			if otelOn {
				fmt.Fprintf(&sb, "recommended order: environment -> status -> observability -> process_logs/investigate\n")
			} else {
				fmt.Fprintf(&sb, "recommended order: environment -> status -> process_logs\n")
				fmt.Fprintf(&sb, "note: observability disabled — no trace/telemetry tools. Turn it on with the observability tool (action=enable).\n")
			}
			if !tunnelsOn {
				fmt.Fprintf(&sb, "note: tunnel tool unavailable — tailscale is not installed on this machine.\n")
			}
		} else {
			fmt.Fprintf(&sb, "tools: status, investigate\n")
			fmt.Fprintf(&sb, "unavailable: restart, stop, configure, process_logs (require the local dev daemon)\n")
		}

		names := sortedEnvKeys(allEnvs)
		envList := make([]string, 0, len(names))
		for _, name := range names {
			env := allEnvs[name]
			entry := fmt.Sprintf("%s(%s)", name, env.Type)
			if name == activeEnvName {
				entry += "*"
			}
			envList = append(envList, entry)
		}
		fmt.Fprintf(&sb, "envs: %s\n", strings.Join(envList, ", "))
		fmt.Fprintf(&sb, "switch: DEVSTACK_ENVIRONMENT=<name>\n")

		return mcp.NewToolResultText(sb.String()), nil
	})
}

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
	for _, s := range stacks {
		if s.Status == "active" {
			parts = append(parts, fmt.Sprintf("%s (active, base :%d)", s.Name, s.BasePort))
		} else {
			parts = append(parts, fmt.Sprintf("%s (%s)", s.Name, s.Status))
		}
	}
	return fmt.Sprintf("workspace: %s — base + %d feature stack(s) in flight: %s\n", ws.Name, len(stacks), strings.Join(parts, ", "))
}
