package cmd

import (
	"devstack/internal/otel"
	_ "devstack/internal/otel/plugins/forwarding" // register forwarding plugin
	_ "devstack/internal/otel/plugins/signoz"     // register signoz plugin
	"devstack/internal/workspace"
)

// activePlugin returns the active otel plugin for the given workspace and environment.
// Resolution order:
//  1. Explicit plugin set in workspace config (ws.OtelPlugin non-empty) — manual config wins
//  2. Environment has an OTLP endpoint → forwarding mode
//  3. Default: forwarding (collector only, no companion infra)
func activePlugin(ws *workspace.Workspace, env workspace.Environment) otel.Plugin {
	if ws.OtelPlugin != "" {
		if p := otel.Get(ws.OtelPlugin); p != nil {
			return p
		}
	}
	if env.Observability.OTLPEndpoint != "" {
		return otel.Get("forwarding")
	}
	return otel.Get("forwarding")
}

// startOtelStack starts the companion infrastructure and the collector for a workspace.
func startOtelStack(ws *workspace.Workspace, plugin otel.Plugin) error {
	if plugin == nil {
		return nil
	}
	if err := plugin.Validate(ws); err != nil {
		return err
	}
	if err := plugin.StartCompanion(ws); err != nil {
		return err
	}
	return otel.StartCollector(ws, plugin)
}

// stopOtelStack stops the collector and companion infrastructure for a workspace.
func stopOtelStack(ws *workspace.Workspace, plugin otel.Plugin) error {
	if err := otel.StopCollector(ws); err != nil {
		return err
	}
	if plugin != nil {
		return plugin.StopCompanion(ws)
	}
	return nil
}
