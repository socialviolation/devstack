package cmd

import (
	"fmt"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/otel"
	_ "github.com/socialviolation/devstack/internal/otel/plugins/forwarding"  // register forwarding plugin
	_ "github.com/socialviolation/devstack/internal/otel/plugins/openobserve" // register openobserve plugin
	_ "github.com/socialviolation/devstack/internal/otel/plugins/signoz"      // register signoz plugin
	"github.com/socialviolation/devstack/internal/workspace"
)

// ensureCollector starts the collector when the workspace has observability
// enabled but it is not running. Returns started=false with no error when there
// is nothing to do (disabled, or already running).
func ensureCollector(ws *workspace.Workspace) (started bool, err error) {
	if !config.ObservabilityEnabled(ws.Path) {
		return false, nil
	}
	plugin := activePlugin(ws)
	if plugin == nil {
		return false, fmt.Errorf("no OTEL plugin configured")
	}
	if isOtelRunning(ws) && !plugin.CompanionStale(ws) {
		return false, nil
	}
	if err := startOtelStack(ws, plugin); err != nil {
		return false, err
	}
	return true, nil
}

// activePlugin returns the active otel plugin for the given workspace: the
// explicit plugin from workspace config when set, otherwise the default.
func activePlugin(ws *workspace.Workspace) otel.Plugin {
	return otel.For(ws)
}

// startOtelStack brings up the workspace's companion infrastructure and folds it
// into the one host collector, which is regenerated from every observability-
// enabled workspace so starting one never drops another's telemetry.
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

	contribs, err := collectorContributions(ws)
	if err != nil {
		return err
	}
	return otel.StartCollector(contribs)
}

// stopOtelStack drops a workspace from the host collector and stops its
// companion. Neither is torn down while another workspace still needs it — both
// are shared by every workspace on the machine.
func stopOtelStack(ws *workspace.Workspace, plugin otel.Plugin) error {
	remaining, err := collectorContributions(nil, ws.Name)
	if err != nil {
		return err
	}

	if len(remaining) == 0 {
		if err := otel.StopCollector(); err != nil {
			return err
		}
	} else if otel.CollectorRunning() {
		if err := otel.StartCollector(remaining); err != nil {
			return err
		}
	}

	if plugin == nil || companionStillNeeded(plugin.Name(), remaining) {
		return nil
	}
	return plugin.StopCompanion(ws)
}

// companionStillNeeded reports whether another workspace is on the same plugin,
// in which case its shared companion must stay up.
func companionStillNeeded(plugin string, remaining []otel.WorkspaceContribution) bool {
	for _, c := range remaining {
		if c.Plugin == plugin {
			return true
		}
	}
	return false
}

// collectorContributions gathers what every observability-enabled workspace
// wants from the one host collector; names in exclude are left out. Every
// enabled workspace contributes, not just the running ones, so the config does
// not depend on the order workspaces were started and starting one never drops
// another's pipelines. An idle workspace costs nothing: routing means no
// telemetry reaches its exporters until its services emit some.
func collectorContributions(include *workspace.Workspace, exclude ...string) ([]otel.WorkspaceContribution, error) {
	excluded := map[string]bool{}
	for _, name := range exclude {
		excluded[name] = true
	}

	all, err := workspace.All()
	if err != nil {
		return nil, err
	}

	var contribs []otel.WorkspaceContribution
	seen := map[string]bool{}

	add := func(ws *workspace.Workspace) error {
		if ws == nil || seen[ws.Name] || excluded[ws.Name] {
			return nil
		}
		if !config.ObservabilityEnabled(ws.Path) {
			return nil
		}
		plugin := otel.For(ws)
		if plugin == nil {
			return nil
		}
		c, err := plugin.Contribute(ws)
		if err != nil {
			return fmt.Errorf("workspace %q (%s): %w", ws.Name, plugin.Name(), err)
		}
		seen[ws.Name] = true
		contribs = append(contribs, otel.WorkspaceContribution{
			Workspace: ws.Name,
			Plugin:    plugin.Name(),
			// A plugin with a local query UI stores telemetry on this machine;
			// one without forwards it somewhere else.
			Local:        plugin.QueryEndpoint(ws) != "",
			Contribution: c,
		})
		return nil
	}

	if err := add(include); err != nil {
		return nil, err
	}
	for i := range all {
		ws := all[i]
		ws.OverlayProjectConfig()
		if err := add(&ws); err != nil {
			return nil, err
		}
	}
	return contribs, nil
}
