// Package forwarding provides a pure-forwarding OTEL plugin for devstack.
// It configures the collector to forward telemetry to an upstream OTLP endpoint
// with optional resource attributes injected (deployment.environment, plus extras).
package forwarding

import (
	"fmt"
	"strings"

	"devstack/internal/otel"
	"devstack/internal/workspace"
)

func init() {
	otel.Register(&ForwardingPlugin{})
}

// ForwardingPlugin forwards telemetry to a remote OTLP endpoint.
// It has no companion infrastructure — CompanionRunning always returns true.
type ForwardingPlugin struct{}

func (p *ForwardingPlugin) Name() string { return "forwarding" }

// CollectorConfig generates YAML to forward telemetry to the configured upstream endpoint.
func (p *ForwardingPlugin) CollectorConfig(ws *workspace.Workspace) ([]byte, error) {
	upstream := ws.PluginConfig("upstream")
	if upstream == "" {
		return nil, fmt.Errorf("forwarding plugin requires 'upstream' config key — run: devstack otel configure --plugin=forwarding --set upstream=https://otel.example.com:4318")
	}

	deploymentEnv := ws.PluginConfig("deployment_env")
	if deploymentEnv == "" {
		deploymentEnv = "dev"
	}

	apiKey := ws.PluginConfig("api_key")
	// api_key_header allows customising the header name (default: Authorization with Bearer prefix).
	// Use "signoz-ingestion-key" for SigNoz cloud, or any other custom header name.
	// When a custom header name is set the key value is sent verbatim (no "Bearer " prefix).
	apiKeyHeader := ws.PluginConfig("api_key_header")

	protocol := ws.PluginConfig("protocol") // "grpc" or "http" (default "http")
	useGRPC := strings.ToLower(protocol) == "grpc"

	// Build resource attribute entries: deployment.environment first, then extras.
	attrLines := fmt.Sprintf("      - action: upsert\n        key: deployment.environment\n        value: %s\n", deploymentEnv)

	// resource_attributes: comma-separated key=value pairs, e.g. "engineer=nick,team=platform"
	if extras := ws.PluginConfig("resource_attributes"); extras != "" {
		for _, pair := range strings.Split(extras, ",") {
			pair = strings.TrimSpace(pair)
			idx := strings.IndexByte(pair, '=')
			if idx < 1 {
				continue
			}
			k := strings.TrimSpace(pair[:idx])
			v := strings.TrimSpace(pair[idx+1:])
			attrLines += fmt.Sprintf("      - action: upsert\n        key: %s\n        value: %s\n", k, v)
		}
	}

	// Build exporter block.
	var exporterName, exporterBlock string
	if useGRPC {
		exporterName = "otlp_grpc"
		// gRPC endpoint: strip https:// or http:// scheme — otelcol gRPC expects host:port only.
		endpoint := upstream
		endpoint = strings.TrimPrefix(endpoint, "https://")
		endpoint = strings.TrimPrefix(endpoint, "http://")

		headersBlock := ""
		if apiKey != "" {
			if apiKeyHeader != "" {
				headersBlock = fmt.Sprintf("    headers:\n      %s: \"%s\"\n", apiKeyHeader, apiKey)
			} else {
				headersBlock = fmt.Sprintf("    headers:\n      Authorization: \"Bearer %s\"\n", apiKey)
			}
		}
		exporterBlock = fmt.Sprintf("exporters:\n  otlp_grpc:\n    endpoint: %s\n%s", endpoint, headersBlock)
	} else {
		exporterName = "otlphttp"
		headersBlock := ""
		if apiKey != "" {
			if apiKeyHeader != "" {
				headersBlock = fmt.Sprintf("    headers:\n      %s: \"%s\"\n", apiKeyHeader, apiKey)
			} else {
				headersBlock = fmt.Sprintf("    headers:\n      Authorization: \"Bearer %s\"\n", apiKey)
			}
		}
		exporterBlock = fmt.Sprintf("exporters:\n  otlphttp:\n    endpoint: %s\n%s", upstream, headersBlock)
	}

	cfg := fmt.Sprintf(`processors:
  resource:
    attributes:
%s  batch: {}

%s
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [%s]
    metrics:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [%s]
    logs:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [%s]
`, attrLines, exporterBlock, exporterName, exporterName, exporterName)

	return []byte(cfg), nil
}

// StartCompanion is a no-op — forwarding has no companion infrastructure.
func (p *ForwardingPlugin) StartCompanion(ws *workspace.Workspace) error { return nil }

// StopCompanion is a no-op.
func (p *ForwardingPlugin) StopCompanion(ws *workspace.Workspace) error { return nil }

// CompanionRunning always returns true — no companion to check.
func (p *ForwardingPlugin) CompanionRunning(ws *workspace.Workspace) bool { return true }

// QueryEndpoint returns "" — forwarding has no local UI.
func (p *ForwardingPlugin) QueryEndpoint(ws *workspace.Workspace) string { return "" }

// Validate checks that the upstream config key is set.
func (p *ForwardingPlugin) Validate(ws *workspace.Workspace) error {
	if ws.PluginConfig("upstream") == "" {
		return fmt.Errorf("forwarding plugin requires 'upstream' config key — run: devstack otel configure --plugin=forwarding --set upstream=https://otel.example.com:4318\nor add an environment with --otlp-endpoint: devstack env add <name> --url=<query-url> --otlp-endpoint=<otlp-url>")
	}
	return nil
}

// ConfigSchema describes the config keys accepted by the forwarding plugin.
func (p *ForwardingPlugin) ConfigSchema() []otel.ConfigField {
	return []otel.ConfigField{
		{
			Key:         "upstream",
			Description: "OTLP endpoint to forward telemetry to (e.g. https://otel.example.com:4318 for HTTP, otel.example.com:4317 for gRPC)",
			Required:    true,
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
