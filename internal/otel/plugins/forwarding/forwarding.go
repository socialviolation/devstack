// Package forwarding provides a pure-forwarding OTEL plugin for devstack.
// It configures the collector to forward telemetry to an upstream OTLP endpoint
// with an optional deployment.environment resource attribute injected.
package forwarding

import (
	"fmt"

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

	cfg := fmt.Sprintf(`processors:
  resource:
    attributes:
      - action: upsert
        key: deployment.environment
        value: %s
  batch: {}

exporters:
  otlphttp:
    endpoint: %s

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [otlphttp]
    metrics:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [otlphttp]
    logs:
      receivers: [otlp]
      processors: [resource, batch]
      exporters: [otlphttp]
`, deploymentEnv, upstream)

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
		return fmt.Errorf("forwarding plugin requires 'upstream' config key — run: devstack otel configure --plugin=forwarding --set upstream=https://otel.example.com:4318")
	}
	return nil
}

// ConfigSchema describes the config keys accepted by the forwarding plugin.
func (p *ForwardingPlugin) ConfigSchema() []otel.ConfigField {
	return []otel.ConfigField{
		{
			Key:         "upstream",
			Description: "OTLP HTTP endpoint to forward telemetry to (e.g. https://otel.example.com:4318)",
			Required:    true,
		},
		{
			Key:         "deployment_env",
			Description: "Value to inject as deployment.environment resource attribute",
			Required:    false,
			Default:     "dev",
		},
	}
}
