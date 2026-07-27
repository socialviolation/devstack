package otel

import (
	"github.com/socialviolation/devstack/internal/observability"
	"github.com/socialviolation/devstack/internal/workspace"
)

// Plugin is the interface that all OTEL backend plugins must implement.
// One collector runs per machine and receives telemetry from every workspace via
// OTLP. The plugin controls what the collector does with a workspace's telemetry
// and optionally manages companion infrastructure (e.g. the OpenObserve container).
type Plugin interface {
	Name() string
	// Contribute returns the collector components and per-signal pipeline shape
	// this plugin needs for one workspace. Core owns the OTLP receiver, merges
	// every active workspace's contribution into one config, and routes by the
	// devstack.workspace resource attribute when they disagree.
	Contribute(ws *workspace.Workspace) (Contribution, error)
	// StartCompanion brings up backing infrastructure (e.g. the OpenObserve container).
	// No-op for pure-forwarding plugins.
	StartCompanion(ws *workspace.Workspace) error
	// StopCompanion tears down backing infrastructure.
	// No-op for pure-forwarding plugins.
	StopCompanion(ws *workspace.Workspace) error
	// CompanionRunning returns true if the companion infrastructure is running.
	// Always true for plugins with no companion.
	CompanionRunning(ws *workspace.Workspace) bool
	// CompanionStale reports whether the companion that exists no longer matches
	// what this build expects — a pinned image that has since moved, say. A
	// running companion is otherwise left alone, so without this an upgrade
	// would silently keep the old one.
	CompanionStale(ws *workspace.Workspace) bool
	// QueryEndpoint returns local observability UI URL, or "" if none.
	QueryEndpoint(ws *workspace.Workspace) string
	// Backend returns a query client for this plugin's telemetry store, already
	// pointed at the right endpoint with the right credentials. Callers never
	// name a backend or supply a URL — see BackendFor. Plugins that store
	// nothing locally return an error saying where the telemetry went instead.
	Backend(ws *workspace.Workspace) (observability.Backend, error)
	// Validate checks prerequisites (docker available, binary on PATH, required config keys set, etc.)
	Validate(ws *workspace.Workspace) error
	// ConfigSchema returns the list of config fields this plugin accepts.
	ConfigSchema() []ConfigField
}

// Contribution is one workspace's share of the host collector config: the
// components it needs declared, and which of them each signal's pipeline runs.
type Contribution struct {
	Exporters  map[string]any
	Processors map[string]any
	Connectors map[string]any
	Extensions map[string]any

	Traces  Pipeline
	Metrics Pipeline
	Logs    Pipeline

	// Extra holds pipelines fed by a connector rather than the OTLP receiver,
	// keyed by full pipeline ID (e.g. "metrics/meter"). Their receivers are
	// declared explicitly and they are never routed.
	Extra map[string]Pipeline

	// Telemetry overrides the collector's own service.telemetry block.
	Telemetry map[string]any
}

// Pipeline names the processors and exporters a signal flows through.
type Pipeline struct {
	Receivers  []string
	Processors []string
	Exporters  []string
}

// ConfigField describes a single plugin configuration key.
type ConfigField struct {
	Key         string
	Description string
	Required    bool
	Default     string
}
