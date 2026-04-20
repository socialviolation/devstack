package otel

import "devstack/internal/workspace"

// Plugin is the interface that all OTEL backend plugins must implement.
// The collector always runs locally and receives telemetry via OTLP.
// The plugin controls what the collector does with that telemetry and
// optionally manages companion infrastructure (e.g. SigNoz docker-compose stack).
type Plugin interface {
	Name() string
	// CollectorConfig returns YAML for exporters+processors+pipelines+extensions+connectors only.
	// Core always injects the OTLP receiver block before writing.
	CollectorConfig(ws *workspace.Workspace) ([]byte, error)
	// StartCompanion brings up backing infrastructure (e.g. SigNoz docker-compose).
	// No-op for pure-forwarding plugins.
	StartCompanion(ws *workspace.Workspace) error
	// StopCompanion tears down backing infrastructure.
	// No-op for pure-forwarding plugins.
	StopCompanion(ws *workspace.Workspace) error
	// CompanionRunning returns true if the companion infrastructure is running.
	// Always true for plugins with no companion.
	CompanionRunning(ws *workspace.Workspace) bool
	// QueryEndpoint returns local observability UI URL, or "" if none.
	QueryEndpoint(ws *workspace.Workspace) string
	// Validate checks prerequisites (docker available, binary on PATH, required config keys set, etc.)
	Validate(ws *workspace.Workspace) error
	// ConfigSchema returns the list of config fields this plugin accepts.
	ConfigSchema() []ConfigField
}

// ConfigField describes a single plugin configuration key.
type ConfigField struct {
	Key         string
	Description string
	Required    bool
	Default     string
}
