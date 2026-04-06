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
				"Environments: local (full control) vs remote (observability-only, no service restart/stop).",
		),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var sb strings.Builder

		// Header — make environment immediately obvious
		fmt.Fprintf(&sb, "=== Active Environment: %s ===\n\n", strings.ToUpper(activeEnvName))
		fmt.Fprintf(&sb, "  Type:    %s\n", activeEnv.Type)
		fmt.Fprintf(&sb, "  Backend: %s\n", activeEnv.Observability.Backend)
		fmt.Fprintf(&sb, "  URL:     %s\n\n", activeEnv.Observability.URL)

		// Capabilities
		if activeEnv.Type == workspace.EnvironmentTypeLocal {
			fmt.Fprintf(&sb, "Available tools (LOCAL — full control):\n")
			fmt.Fprintf(&sb, "  status          — show all service states (running/error/building/disabled)\n")
			fmt.Fprintf(&sb, "  restart         — restart a service\n")
			fmt.Fprintf(&sb, "  stop            — stop a service\n")
			fmt.Fprintf(&sb, "  configure       — set Tilt runtime arguments\n")
			fmt.Fprintf(&sb, "  process_logs    — fetch stdout/stderr from a service\n")
			fmt.Fprintf(&sb, "  investigate     — explore distributed traces\n")
			fmt.Fprintf(&sb, "  environment     — (this tool)\n")
		} else {
			fmt.Fprintf(&sb, "Available tools (REMOTE — read-only, no service control):\n")
			fmt.Fprintf(&sb, "  status          — show service health from recent trace activity\n")
			fmt.Fprintf(&sb, "  investigate     — explore distributed traces\n")
			fmt.Fprintf(&sb, "  environment     — (this tool)\n")
			fmt.Fprintf(&sb, "\nNOT available in remote environments:\n")
			fmt.Fprintf(&sb, "  restart, stop, configure, process_logs\n")
			fmt.Fprintf(&sb, "  (These require Tilt, which only runs locally)\n")
		}

		// All environments
		fmt.Fprintf(&sb, "\nAll environments for workspace %q:\n", workspaceName)
		names := sortedEnvKeys(allEnvs)
		for _, name := range names {
			env := allEnvs[name]
			marker := ""
			if name == activeEnvName {
				marker = "  <- active"
			}
			fmt.Fprintf(&sb, "  %-12s %-8s %s%s\n", name, env.Type, env.Observability.URL, marker)
		}

		fmt.Fprintf(&sb, "\nTo switch environment: set DEVSTACK_ENVIRONMENT=<name> in your .mcp.json env block\n")

		return mcp.NewToolResultText(sb.String()), nil
	})
}
