package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/telemetry"
	"github.com/socialviolation/devstack/internal/workspace"
)

// registerObservabilityTool exposes read + write control over the workspace's
// OTEL configuration. It is always registered for local environments — even when
// observability is disabled — so an agent can discover the capability and turn it
// on. The trace-query tool (investigate) remains gated on it being enabled;
// per-service telemetry evidence is reported by this tool's status action.
func registerObservabilityTool(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("observability",
		mcp.WithDescription("Inspect and change this workspace's OpenTelemetry (OTEL) configuration. "+
			"When enabled, `devstack workspace up` runs a local collector and OTEL export env is pushed down to services; "+
			"when disabled, services are not assumed to be instrumented and no collector runs. "+
			"The trace-query tool (investigate) only exists while observability is enabled — use action='enable' to turn it on. "+
			"action='status' reports evidence per running variant — for each service, which instances (base, each feature stack) actually emitted spans in the last 15 minutes, with the env each runs under and the name it reports itself as. Check it before concluding a service or a stack is silent. "+
			"Actions: 'status' (current enabled state, backend, collector), 'enable' (turn on, optionally set backend), 'disable' (turn off), "+
			"'configure' (set the backend and/or a plugin config key such as upstream). "+
			"Config changes take effect on the next `devstack otel start` / `workspace up`; the available MCP tool set updates when the MCP server restarts."),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("One of: status, enable, disable, configure.")),
		mcp.WithString("backend",
			mcp.Description("Backend/plugin to use (e.g. 'openobserve', 'signoz', 'forwarding'). Optional for enable/configure; defaults to openobserve — a single lightweight local stack shared by every workspace.")),
		mcp.WithString("key",
			mcp.Description("Plugin config key to set with 'configure' (e.g. 'upstream', 'deployment_env'). Requires value.")),
		mcp.WithString("value",
			mcp.Description("Value for the given config key.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved"), nil
		}
		action := strings.ToLower(req.GetString("action", ""))
		backend := req.GetString("backend", "")
		key := req.GetString("key", "")
		value := req.GetString("value", "")

		switch action {
		case "status":
			return mcp.NewToolResultText(observabilityStatus(ws, workspacePath)), nil

		case "enable":
			if err := config.SetObservabilityEnabled(workspacePath, true); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if backend != "" {
				if err := config.SetObservabilityBackend(workspacePath, backend); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
			}
			return mcp.NewToolResultText("Observability enabled. Run `devstack otel start` (or restart the workspace) to start the collector.\n\n" + observabilityStatus(ws, workspacePath)), nil

		case "disable":
			if err := config.SetObservabilityEnabled(workspacePath, false); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("Observability disabled. No collector will start on the next `workspace up`.\n\n" + observabilityStatus(ws, workspacePath)), nil

		case "configure":
			if backend == "" && key == "" {
				return mcp.NewToolResultError("configure needs a backend and/or a key=value plugin config"), nil
			}
			if backend != "" {
				if err := config.SetObservabilityBackend(workspacePath, backend); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
			}
			if key != "" {
				if value == "" {
					return mcp.NewToolResultError("configure with a key also requires a value"), nil
				}
				merged := map[string]string{}
				for k, v := range ws.OtelPluginConfig {
					merged[k] = v
				}
				merged[key] = value
				if err := workspace.UpdateOtelPlugin(ws.Name, backend, merged); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
			}
			return mcp.NewToolResultText("Observability configured.\n\n" + observabilityStatus(ws, workspacePath)), nil

		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use status, enable, disable, or configure", action)), nil
		}
	})
}

// observabilityStatus renders the current OTEL config + runtime state, reading
// the manifest fresh so it reflects changes made during this session.
func observabilityStatus(ws *workspace.Workspace, workspacePath string) string {
	enabled := config.ObservabilityEnabled(workspacePath)

	backend := "none"
	if rw, err := config.ResolveWorkspace(workspacePath); err == nil && rw.Manifest != nil {
		if b := rw.Manifest.Observability.ResolvedBackend(); b != "" {
			backend = b
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "enabled: %t\n", enabled)
	fmt.Fprintf(&sb, "backend: %s\n", backend)
	if !enabled {
		fmt.Fprintf(&sb, "collector: not applicable (disabled)\n")
		fmt.Fprintf(&sb, "note: enable to run a collector and expose the investigate tool.\n")
		return sb.String()
	}
	fmt.Fprintf(&sb, "collector: %s\n", runningLabel(otel.CollectorRunning()))
	fmt.Fprintf(&sb, "otlp: grpc=localhost:%d http=localhost:%d\n", workspace.OTLPGRPCPort, workspace.OTLPHTTPPort)
	if upstream := ws.PluginConfig("upstream"); upstream != "" {
		fmt.Fprintf(&sb, "upstream: %s\n", upstream)
	}

	// Per-service telemetry evidence — whether signals are actually arriving.
	// Check this before inferring anything from missing traces or logs.
	evidenceBackend, _ := otel.BackendFor(ws)
	if statuses, err := telemetry.Status(workspacePath, evidenceBackend, telemetry.DefaultWindow); err == nil && len(statuses) > 0 {
		fmt.Fprintf(&sb, "evidence (last %s, per variant):\n", telemetry.DefaultWindow)
		for _, s := range statuses {
			fmt.Fprintf(&sb, "  %s: confidence=%s spans=%d mode=%s\n", s.Service, s.Confidence, s.TraceCount, s.Mode)
			fmt.Fprintf(&sb, "    %s\n", s.Summary())
		}
	}
	return sb.String()
}

func runningLabel(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}
