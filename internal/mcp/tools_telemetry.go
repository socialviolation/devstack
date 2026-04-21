package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"devstack/internal/telemetry"
)

func registerTelemetryHealthTool(mcpServer *server.MCPServer, workspacePath string) {
	tool := mcp.NewTool("telemetry_health",
		mcp.WithDescription("Show telemetry evidence and confidence for local services. Use this before inferring from missing traces or logs. Reports expected signals, collector reachability, observed traces/log evidence, confidence, and an interpretation note."),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		statuses, err := telemetry.Status(workspacePath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var sb strings.Builder
		for _, status := range statuses {
			fmt.Fprintf(&sb, "%s: confidence=%s traces_expected=%t logs_expected=%t collector_reachable=%t traces=%d logs=%t mode=%s\n", status.Service, status.Confidence, status.ExpectedTraces, status.ExpectedLogs, status.CollectorReachable, status.TraceCount, status.LogEvidence, status.Mode)
			fmt.Fprintf(&sb, "  %s\n", status.Interpretation)
		}
		return mcp.NewToolResultText(strings.TrimSpace(sb.String())), nil
	})
}
