package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// registerEnvironmentTool registers the "environment" tool which orients agents immediately.
// This is the MOST IMPORTANT discoverability tool — calling it first reveals what's possible.
func registerEnvironmentTool(mcpServer *server.MCPServer, obsURL, workspaceName, workspacePath string, ws *workspace.Workspace) {
	tool := mcp.NewTool("environment",
		mcp.WithDescription(
			"Show the active workspace and available tools. "+
				"Call this first to understand what you can and cannot do in the current context. "+
				"An 'env' here is a CONFIG-PATCH environment — a named set of config vars (e.g. 'staging') that a workspace, service, or stack instance is pointed at via env_use (CLI: devstack env use). status and env_which show which env each instance currently points at. "+
				"devstack is a LOCAL development environment. Data is ephemeral and local — not production. "+
				"The available tools depend on this workspace's configuration: trace/telemetry tools appear only when observability is enabled, tunnel tools only when tailscale is installed. "+
				"Call this tool first to understand the context before using other tools.",
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var sb strings.Builder

		otelOn := config.ObservabilityEnabled(workspacePath)
		tunnelsOn := tailscaleInstalled()

		backend := "signoz"
		if !otelOn {
			backend = "none"
		}
		if otelOn {
			fmt.Fprintf(&sb, "observability: %s@%s + local dev daemon — local dev only, ephemeral data\n", backend, obsURL)
		} else {
			fmt.Fprintf(&sb, "observability: local dev daemon (disabled) — local dev only, ephemeral data\n")
		}

		if line := stacksSummary(ws); line != "" {
			sb.WriteString(line)
		}
		tools := []string{"status", "restart", "stop", "configure", "process_logs", "service_env", "observability", "stack_create", "stack_list", "stack_up", "stack_down", "stack_rm", "env_use", "env_which", "env_set"}
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
