package cmd

import (
	"fmt"

	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/otel"
	_ "github.com/socialviolation/devstack/internal/otel/plugins/forwarding" // register forwarding plugin
	_ "github.com/socialviolation/devstack/internal/otel/plugins/signoz"     // register signoz plugin
	"github.com/socialviolation/devstack/internal/workspace"
)

// ensureCollector starts the collector when the workspace has observability
// enabled but it isn't running. Returns started=false with no error when there
// is nothing to do (disabled, or already running).
func ensureCollector(ws *workspace.Workspace) (started bool, err error) {
	if !config.ObservabilityEnabled(ws.Path) || isOtelRunning(ws) {
		return false, nil
	}
	envName := viper.GetString("environment")
	if envName == "" {
		envName = "local"
	}
	env, _ := ws.ResolveEnvironment(envName)
	plugin := activePlugin(ws, env)
	if plugin == nil {
		return false, fmt.Errorf("no OTEL plugin configured")
	}
	if env.Observability.OTLPEndpoint != "" && ws.OtelPlugin == "" {
		wsCopy := *ws
		cfg := map[string]string{}
		for k, v := range ws.OtelPluginConfig {
			cfg[k] = v
		}
		cfg["upstream"] = env.Observability.OTLPEndpoint
		if env.Observability.APIKey != "" {
			cfg["api_key"] = env.Observability.APIKey
		}
		cfg["deployment_env"] = envName
		wsCopy.OtelPluginConfig = cfg
		ws = &wsCopy
	}
	if err := startOtelStack(ws, plugin); err != nil {
		return false, err
	}
	return true, nil
}

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
