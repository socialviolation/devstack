package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"devstack/internal/workspace"
)

// registerEnvironmentTool registers the "environment" tool which orients agents immediately.
// This is the MOST IMPORTANT discoverability tool — calling it first reveals what's possible.
func registerEnvironmentTool(mcpServer *server.MCPServer, activeEnvName string,
	activeEnv workspace.Environment, allEnvs map[string]workspace.Environment, workspaceName string) {

	tool := mcp.NewTool("environment",
		mcp.WithDescription(
			"Show the active environment and available tools. "+
				"Call this first to understand what you can and cannot do in the current context. "+
				"Environments: local (full control) vs remote (observability-only, no service restart/stop). "+
				"devstack is a LOCAL development environment. Data is ephemeral and local — not production. "+
				"Observability: traces and metrics are collected by SignOz (ClickHouse backend). Service orchestration is via Tilt. "+
				"Call this tool first to understand the context before using other tools.",
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var sb strings.Builder

		backend := activeEnv.Observability.Backend
		if backend == "" {
			backend = "signoz"
		}
		if activeEnv.Observability.OTLPEndpoint != "" {
			fmt.Fprintf(&sb, "env: %s (%s) %s@%s  otlp->%s\n", activeEnvName, activeEnv.Type, backend, activeEnv.Observability.URL, activeEnv.Observability.OTLPEndpoint)
		} else {
			fmt.Fprintf(&sb, "env: %s (%s) %s@%s\n", activeEnvName, activeEnv.Type, backend, activeEnv.Observability.URL)
		}
		fmt.Fprintf(&sb, "stack: SignOz (ClickHouse) + Tilt — local dev only, ephemeral data\n")

		if activeEnv.Type == workspace.EnvironmentTypeLocal {
			fmt.Fprintf(&sb, "tools: status, restart, stop, configure, process_logs, investigate\n")
		} else {
			fmt.Fprintf(&sb, "tools: status, investigate\n")
			fmt.Fprintf(&sb, "unavailable: restart, stop, configure, process_logs (require Tilt; local only)\n")
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
