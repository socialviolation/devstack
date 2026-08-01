package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/observability"
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
		mcp.WithDescription("Inspect this workspace's OpenTelemetry (OTEL) setup, check whether telemetry is arriving, and change the OTEL configuration.\n"+
			"Actions:\n"+
			"- 'status' — reads. Enabled state, backend, whether the collector runs, the OTLP ports, then evidence for every service this workspace declares: which of its variants (base, each feature stack) emitted spans in the last 15 minutes, the env each ran under, the name it reports itself as, a span count and a confidence rating. This is the answer to \"is my service sending telemetry?\". It only covers services declared in this workspace's config.\n"+
			"- 'variants' — reads. The backend's own list of every service variant that reported telemetry in the last 15 minutes, with stack, env and span count, whether or not devstack has a manifest for it. Same data as `devstack otel services`. Use it when 'status' shows a service as silent, to see what reports instead — a different reported name, another stack, another workspace's copy.\n"+
			"- 'config_on' — writes config: sets enabled=true, optionally with a backend (CLI: `devstack otel config on`).\n"+
			"- 'config_off' — writes config: sets enabled=false (CLI: `devstack otel config off`).\n"+
			"- 'config_set' — writes config: sets the backend and/or a plugin config key such as upstream (CLI: `devstack otel config set`).\n"+
			"The three config_* actions write this workspace's manifest and nothing else — they start no process and stop none. Running and killing the collector is `devstack otel start` / `devstack otel stop` in a shell; there is no MCP action for it.\n"+
			"'config_on' also grants no new tools now. The collector starts on the next `devstack otel start` or `devstack workspace up`. The trace-query tool (investigate) is registered only when observability is enabled and the registration happens at MCP server startup, so it will not appear in this session — the MCP server has to be restarted. Sequence: config_on, start the collector, restart the MCP server, then query traces.\n"+
			"While observability is disabled no collector runs and no OTEL export env is pushed down to services."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
		mcp.WithString("action", mcp.Required(),
			mcp.Description("One of: status, variants — read only, change nothing; config_on, config_off, config_set — each writes this workspace's manifest and starts or stops no process.")),
		mcp.WithString("backend",
			mcp.Description("Backend/plugin to use (for example 'openobserve', 'signoz', 'forwarding'). Optional for config_on/config_set; defaults to openobserve — a single lightweight local stack shared by every workspace.")),
		mcp.WithString("key",
			mcp.Description("Plugin config key to set with 'config_set' (for example 'upstream', 'deployment_env'). Requires value.")),
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

		case "variants":
			return mcp.NewToolResultText(observabilityVariants(ws)), nil

		case "config_on":
			if err := config.SetObservabilityEnabled(workspacePath, true); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if backend != "" {
				if err := config.SetObservabilityBackend(workspacePath, backend); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
			}
			return mcp.NewToolResultText("Observability turned on in config. Nothing is running yet — run `devstack otel start` (or restart the workspace) to start the collector.\n\n" + observabilityStatus(ws, workspacePath)), nil

		case "config_off":
			if err := config.SetObservabilityEnabled(workspacePath, false); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("Observability turned off in config. No collector will start on the next `workspace up`; one running now keeps running until `devstack otel stop`.\n\n" + observabilityStatus(ws, workspacePath)), nil

		case "config_set":
			if backend == "" && key == "" {
				return mcp.NewToolResultError("config_set needs a backend and/or a key=value plugin config"), nil
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
			return mcp.NewToolResultText("Observability configured. A running collector keeps its old settings until `devstack otel stop` then `devstack otel start`.\n\n" + observabilityStatus(ws, workspacePath)), nil

		default:
			return mcp.NewToolResultError(fmt.Sprintf("unknown action %q — use status, variants, config_on, config_off, or config_set", action)), nil
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

	// Per-service telemetry evidence — whether signals are arriving.
	// Check this before inferring anything from missing traces or logs.
	evidenceBackend, _ := otel.BackendFor(ws)
	if statuses, err := telemetry.Status(workspacePath, evidenceBackend, telemetry.DefaultWindow); err == nil && len(statuses) > 0 {
		fmt.Fprintf(&sb, "evidence (last %s, per variant):\n", telemetry.DefaultWindow)
		fmt.Fprintf(&sb, "  confidence is one of high — spans arrived, so that service is instrumented and reaching the backend; "+
			"partial — logs but no spans, or a scenario mode suppressing traces; "+
			"low — expected telemetry never arrived, or the service declares no expectations; "+
			"inconclusive — no queryable backend, or export deliberately degraded. Only high proves instrumentation.\n")
		for _, s := range statuses {
			fmt.Fprintf(&sb, "  %s: confidence=%s spans=%d mode=%s\n", s.Service, s.Confidence, s.TraceCount, s.Mode)
			fmt.Fprintf(&sb, "    %s\n", s.Summary())
		}
	}
	return sb.String()
}

// observabilityVariants lists what the backend itself says is reporting, which
// is not the same question status answers: status walks the services this
// workspace declares, so a variant devstack holds no manifest for — another
// stack's copy, a service reporting under a name nothing matches — is only
// visible here.
func observabilityVariants(ws *workspace.Workspace) string {
	backend, err := otel.BackendFor(ws)
	if err != nil {
		return fmt.Sprintf("no queryable backend: %v\n", err)
	}
	variants, err := backend.ListVariants(context.Background(), observability.ServiceQuery{Since: telemetry.DefaultWindow})
	if err != nil {
		return fmt.Sprintf("backend query failed: %v\n", err)
	}
	return renderVariants(variants)
}

func renderVariants(variants []observability.ServiceVariant) string {
	if len(variants) == 0 {
		return fmt.Sprintf("No variant reported telemetry in the last %s.\n", telemetry.DefaultWindow)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "variants reporting in the last %s:\n", telemetry.DefaultWindow)
	for _, v := range variants {
		name := v.Service
		if name == "" {
			name = v.Devstack
		}
		fmt.Fprintf(&sb, "  %s", name)
		if v.Devstack != "" && v.Devstack != v.Service {
			fmt.Fprintf(&sb, " (devstack: %s)", v.Devstack)
		}
		stack := v.Stack
		if stack == "" {
			stack = "base"
		}
		fmt.Fprintf(&sb, " stack=%s", stack)
		if v.Env != "" {
			fmt.Fprintf(&sb, " env=%s", v.Env)
		}
		fmt.Fprintf(&sb, " spans=%d\n", v.Spans)
	}
	return sb.String()
}

func runningLabel(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}
