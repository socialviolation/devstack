package cmd

import (
	"devstack/internal/otel"
	_ "devstack/internal/otel/plugins/forwarding" // register forwarding plugin
	_ "devstack/internal/otel/plugins/signoz"     // register signoz plugin
	"devstack/internal/workspace"
)

// activePlugin returns the active otel plugin for the given workspace.
func activePlugin(ws *workspace.Workspace) otel.Plugin {
	return otel.Get(ws.OtelPlugin)
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
