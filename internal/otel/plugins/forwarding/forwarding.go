// Package forwarding provides a pure-forwarding OTEL plugin for devstack.
// It configures the collector to forward telemetry to an upstream OTLP endpoint
// with optional resource attributes injected (deployment.environment, plus extras).
package forwarding

import (
	"fmt"
	"strings"

	"github.com/socialviolation/devstack/internal/observability"

	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/workspace"
)

func init() {
	otel.Register(&ForwardingPlugin{})
}

// ForwardingPlugin forwards telemetry to a remote OTLP endpoint.
// It has no companion infrastructure — CompanionRunning always returns true.
type ForwardingPlugin struct{}

func (p *ForwardingPlugin) Name() string { return "forwarding" }

// Contribute forwards this workspace's telemetry to its configured upstream,
// stamping deployment.environment and any extra resource attributes on the way
// out. With no upstream configured it falls back to the debug exporter so
// telemetry lands in the collector log rather than being silently dropped.
func (p *ForwardingPlugin) Contribute(ws *workspace.Workspace) (otel.Contribution, error) {
	upstream := ws.PluginConfig("upstream")

	if upstream == "" {
		debug := otel.Pipeline{Exporters: []string{"debug"}}
		return otel.Contribution{
			Exporters: map[string]any{"debug": map[string]any{"verbosity": "detailed"}},
			Traces:    debug,
			Metrics:   debug,
			Logs:      debug,
		}, nil
	}

	deploymentEnv := ws.PluginConfig("deployment_env")
	if deploymentEnv == "" {
		deploymentEnv = "dev"
	}

	attrs := []any{
		map[string]any{"action": "upsert", "key": "deployment.environment", "value": deploymentEnv},
	}
	// resource_attributes: comma-separated key=value pairs, e.g. "engineer=nick,team=platform"
	if extras := ws.PluginConfig("resource_attributes"); extras != "" {
		for _, pair := range strings.Split(extras, ",") {
			pair = strings.TrimSpace(pair)
			idx := strings.IndexByte(pair, '=')
			if idx < 1 {
				continue
			}
			attrs = append(attrs, map[string]any{
				"action": "upsert",
				"key":    strings.TrimSpace(pair[:idx]),
				"value":  strings.TrimSpace(pair[idx+1:]),
			})
		}
	}

	apiKey := ws.PluginConfig("api_key")
	// api_key_header allows customising the header name (default: Authorization with Bearer prefix).
	// Use "signoz-ingestion-key" for SigNoz cloud, or any other custom header name.
	// When a custom header name is set the key value is sent verbatim (no "Bearer " prefix).
	apiKeyHeader := ws.PluginConfig("api_key_header")
	headers := map[string]any{}
	if apiKey != "" {
		if apiKeyHeader != "" {
			headers[apiKeyHeader] = apiKey
		} else {
			headers["Authorization"] = "Bearer " + apiKey
		}
	}

	useGRPC := strings.ToLower(ws.PluginConfig("protocol")) == "grpc"
	exporterName := "otlphttp"
	endpoint := upstream
	if useGRPC {
		exporterName = "otlp_grpc"
		// gRPC endpoint: strip https:// or http:// scheme — otelcol gRPC expects host:port only.
		endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	}

	exporter := map[string]any{"endpoint": endpoint}
	if len(headers) > 0 {
		exporter["headers"] = headers
	}

	pipeline := otel.Pipeline{
		Processors: []string{"resource", "batch"},
		Exporters:  []string{exporterName},
	}

	return otel.Contribution{
		Processors: map[string]any{
			"resource": map[string]any{"attributes": attrs},
			"batch":    map[string]any{},
		},
		Exporters: map[string]any{exporterName: exporter},
		Traces:    pipeline,
		Metrics:   pipeline,
		Logs:      pipeline,
	}, nil
}

// StartCompanion is a no-op — forwarding has no companion infrastructure.
func (p *ForwardingPlugin) StartCompanion(ws *workspace.Workspace) error { return nil }

// StopCompanion is a no-op.
func (p *ForwardingPlugin) StopCompanion(ws *workspace.Workspace) error { return nil }

// CompanionStale is always false — there is no companion to go stale.
func (p *ForwardingPlugin) CompanionStale(ws *workspace.Workspace) bool { return false }

// CompanionRunning always returns true — no companion to check.
func (p *ForwardingPlugin) CompanionRunning(ws *workspace.Workspace) bool { return true }

// QueryEndpoint returns "" — forwarding has no local UI.
func (p *ForwardingPlugin) QueryEndpoint(ws *workspace.Workspace) string { return "" }

// Backend reports where the telemetry went, since forwarding keeps nothing
// locally to query.
func (p *ForwardingPlugin) Backend(ws *workspace.Workspace) (observability.Backend, error) {
	if upstream := ws.PluginConfig("upstream"); upstream != "" {
		return nil, fmt.Errorf("workspace %q forwards telemetry to %s — query it there, or switch to a local backend with: devstack otel configure --plugin=openobserve", ws.Name, upstream)
	}
	return nil, fmt.Errorf("workspace %q has no upstream configured — telemetry goes to the collector log. Switch to a local backend with: devstack otel configure --plugin=openobserve", ws.Name)
}

// Validate always passes — upstream is optional. When not set the collector
// runs in debug mode and writes telemetry to stdout.
func (p *ForwardingPlugin) Validate(ws *workspace.Workspace) error {
	return nil
}

// ConfigSchema describes the config keys accepted by the forwarding plugin.
func (p *ForwardingPlugin) ConfigSchema() []otel.ConfigField {
	return []otel.ConfigField{
		{
			Key:         "upstream",
			Description: "OTLP endpoint to forward telemetry to (e.g. https://otel.example.com:4318 for HTTP, otel.example.com:4317 for gRPC). When not set, collector runs in debug mode and writes telemetry to stdout.",
			Required:    false,
		},
		{
			Key:         "protocol",
			Description: "Transport protocol: \"grpc\" or \"http\" (default: http)",
			Required:    false,
			Default:     "http",
		},
		{
			Key:         "deployment_env",
			Description: "Value to inject as deployment.environment resource attribute (default: dev)",
			Required:    false,
			Default:     "dev",
		},
		{
			Key:         "resource_attributes",
			Description: "Extra resource attributes to inject, comma-separated key=value pairs (e.g. engineer=nick,team=platform)",
			Required:    false,
		},
		{
			Key:         "api_key",
			Description: "API key sent as a header to the upstream endpoint",
			Required:    false,
		},
		{
			Key:         "api_key_header",
			Description: "Header name for the API key (default: Authorization with Bearer prefix). Use e.g. signoz-ingestion-key for SigNoz cloud.",
			Required:    false,
		},
	}
}
