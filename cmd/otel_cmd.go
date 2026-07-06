package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/telemetry"
	"github.com/socialviolation/devstack/internal/workspace"
)

var otelCmd = &cobra.Command{
	Use:   "otel",
	Short: "Manage the local observability stack (traces and logs)",
	Long: `devstack runs a local otelcol-contrib collector per workspace. Every service
registered with 'devstack init' is pre-configured to ship OpenTelemetry traces
and logs to this collector via OTEL_EXPORTER_OTLP_ENDPOINT (gRPC on port 4317).

By default the collector runs in debug mode — telemetry is written to the collector
stdout and not forwarded anywhere. Configure an upstream to route telemetry:

  devstack otel configure --plugin=forwarding --set upstream=<host:port> --set protocol=grpc
  devstack otel configure --plugin=signoz      # opt-in: local SigNoz UI via Docker

The stack starts automatically when you run 'devstack workspace up'.

SUBCOMMANDS
  devstack otel enable             turn observability on for this workspace
  devstack otel disable            turn observability off for this workspace
  devstack otel status             collector state, ports, and per-service telemetry evidence
  devstack otel start              start the collector + companion stack
  devstack otel stop               stop the collector + companion stack
  devstack otel open               open the observability UI in the browser
  devstack otel configure          configure the active plugin (backend, upstream)
  devstack otel plugins            list all registered plugins`,
}

var otelStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the managed observability stack",
	Long: `Start the local otelcol-contrib collector and companion stack for the current workspace.

The active plugin controls what the collector does with the telemetry.
Use 'devstack otel configure' to change the plugin.

This is called automatically by 'devstack workspace up'.`,
	RunE: runOtelStart,
}

var otelStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the managed observability stack",
	RunE:  runOtelStop,
}

var otelStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show collector state, ports, and per-service telemetry evidence",
	RunE:  runOtelStatus,
}

var otelOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the observability UI in the browser",
	RunE:  runOtelOpen,
}

var otelConfigureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Configure the active OTEL plugin for the current workspace",
	Long: `Configure the active OTEL plugin for the current workspace.

The --plugin (backend) is written to the workspace manifest so it persists;
--set values (upstream, api keys, etc.) are stored in per-machine config.

Examples:
  devstack otel configure --plugin=signoz
  devstack otel configure --plugin=forwarding --set upstream=https://otel.example.com:4318 --set deployment_env=dev`,
	RunE: runOtelConfigure,
}

var otelEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable observability for the current workspace",
	Long: `Turn observability on for the workspace: 'devstack workspace up' will start a
collector and OTEL export env is pushed down to services. Writes
observability.enabled: true to the workspace manifest.

Examples:
  devstack otel enable
  devstack otel enable --backend=signoz`,
	RunE: runOtelEnable,
}

var otelDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable observability for the current workspace",
	Long: `Turn observability off for the workspace: no collector is started and services
are not assumed to be OTEL-instrumented. Writes observability.enabled: false to
the workspace manifest.`,
	RunE: runOtelDisable,
}

var otelPluginsCmd = &cobra.Command{
	Use:   "plugins",
	Short: "List all registered OTEL plugins",
	RunE:  runOtelPlugins,
}

func init() {
	rootCmd.AddCommand(otelCmd)
	otelCmd.AddCommand(otelEnableCmd)
	otelCmd.AddCommand(otelDisableCmd)
	otelCmd.AddCommand(otelStartCmd)
	otelCmd.AddCommand(otelStopCmd)
	otelCmd.AddCommand(otelStatusCmd)
	otelCmd.AddCommand(otelOpenCmd)
	otelCmd.AddCommand(otelConfigureCmd)
	otelCmd.AddCommand(otelPluginsCmd)

	for _, sub := range []*cobra.Command{otelEnableCmd, otelDisableCmd, otelStartCmd, otelStopCmd, otelStatusCmd, otelOpenCmd, otelConfigureCmd} {
		sub.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	}
	otelEnableCmd.Flags().String("backend", "", "Observability backend to use (default: signoz)")

	// Port flags — stored in workspace config so they persist.
	otelStartCmd.Flags().Int("ui-port", 0, "SigNoz UI + query API port (default 3301)")
	otelStartCmd.Flags().Int("otlp-grpc-port", 0, "OTLP gRPC ingestion port (default 4317)")
	otelStartCmd.Flags().Int("otlp-http-port", 0, "OTLP HTTP ingestion port (default 4318)")

	// Configure flags
	otelConfigureCmd.Flags().String("plugin", "", "Plugin name to activate (e.g. signoz, forwarding)")
	otelConfigureCmd.Flags().StringArray("set", nil, "Set a plugin config key (format: key=value, repeatable)")
}

func resolveOtelWorkspace(cmd *cobra.Command) (*workspace.Workspace, error) {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	var (
		ws  *workspace.Workspace
		err error
	)
	if wsFlag != "" {
		ws, err = resolveWorkspace(wsFlag)
	} else {
		ws, err = workspace.DetectFromCwd()
	}
	if err != nil {
		return nil, fmt.Errorf("could not detect workspace: %w\nTry: devstack otel <subcommand> --workspace=<name>", err)
	}
	ws.OverlayProjectConfig()
	return ws, nil
}

func isOtelRunning(ws *workspace.Workspace) bool {
	// Use a synthesized local env for running checks — the collector is running
	// regardless of which env drove its start. CompanionRunning for forwarding
	// always returns true; for signoz it checks docker-compose.
	localEnv, _ := ws.ResolveEnvironment("local")
	plugin := activePlugin(ws, localEnv)
	if plugin == nil {
		return false
	}
	return otel.CollectorRunning(ws) && plugin.CompanionRunning(ws)
}

func runOtelStart(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	// Apply any port overrides from flags before starting.
	uiPort, _ := cmd.Flags().GetInt("ui-port")
	grpcPort, _ := cmd.Flags().GetInt("otlp-grpc-port")
	httpPort, _ := cmd.Flags().GetInt("otlp-http-port")
	if uiPort > 0 || grpcPort > 0 || httpPort > 0 {
		if err := workspace.UpdateOtelPorts(ws.Name, uiPort, grpcPort, httpPort); err != nil {
			return fmt.Errorf("failed to save port config: %w", err)
		}
		ws, err = resolveOtelWorkspace(cmd)
		if err != nil {
			return err
		}
	}

	// Resolve the active environment to drive plugin selection.
	envName := viper.GetString("environment")
	if envName == "" {
		envName = "local"
	}
	env, ok := ws.ResolveEnvironment(envName)
	if !ok {
		env, _ = ws.ResolveEnvironment("local")
		envName = "local"
	}

	plugin := activePlugin(ws, env)
	if plugin == nil {
		return fmt.Errorf("no OTEL plugin registered — this is a bug")
	}

	// If the environment drives forwarding mode, populate plugin config from env
	// into a local (in-memory only) copy of the workspace. Never saved to disk.
	if env.Observability.OTLPEndpoint != "" && ws.OtelPlugin != "signoz" {
		wsCopy := *ws
		if wsCopy.OtelPluginConfig == nil {
			wsCopy.OtelPluginConfig = map[string]string{}
		} else {
			// shallow copy the map so we don't mutate the original
			copied := make(map[string]string, len(wsCopy.OtelPluginConfig))
			for k, v := range wsCopy.OtelPluginConfig {
				copied[k] = v
			}
			wsCopy.OtelPluginConfig = copied
		}
		wsCopy.OtelPluginConfig["upstream"] = env.Observability.OTLPEndpoint
		if env.Observability.APIKey != "" {
			wsCopy.OtelPluginConfig["api_key"] = env.Observability.APIKey
		}
		wsCopy.OtelPluginConfig["deployment_env"] = envName
		ws = &wsCopy
	}

	if isOtelRunning(ws) {
		queryEndpoint := plugin.QueryEndpoint(ws)
		if queryEndpoint != "" {
			fmt.Printf("OTEL stack already running for '%s' (plugin: %s) — %s\n", ws.Name, plugin.Name(), queryEndpoint)
		} else {
			fmt.Printf("OTEL stack already running for '%s' (plugin: %s)\n", ws.Name, plugin.Name())
		}
		return nil
	}

	// Validate prerequisites
	if err := plugin.Validate(ws); err != nil {
		return fmt.Errorf("plugin validation failed: %w", err)
	}

	fmt.Printf("Starting OTEL stack for '%s' (plugin: %s)...\n", ws.Name, plugin.Name())

	// Start companion infrastructure
	if err := plugin.StartCompanion(ws); err != nil {
		return fmt.Errorf("failed to start companion: %w", err)
	}

	// Start collector
	if err := otel.StartCollector(ws, plugin); err != nil {
		return fmt.Errorf("failed to start collector: %w", err)
	}

	queryEndpoint := plugin.QueryEndpoint(ws)
	fmt.Printf("  plugin:   %s\n", plugin.Name())
	fmt.Printf("  otlp:     http://localhost:%d (HTTP)\n", ws.HTTPPort())
	fmt.Printf("  grpc:     localhost:%d\n", ws.GRPCPort())
	if queryEndpoint != "" {
		fmt.Printf("  ui:       %s\n", queryEndpoint)
	}
	return nil
}

func runOtelStop(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	localEnv, _ := ws.ResolveEnvironment("local")
	plugin := activePlugin(ws, localEnv)

	if !isOtelRunning(ws) {
		fmt.Printf("OTEL stack is not running for '%s'\n", ws.Name)
		return nil
	}

	fmt.Printf("Stopping OTEL stack for '%s'...", ws.Name)

	// Stop collector first
	if err := otel.StopCollector(ws); err != nil {
		fmt.Println(" collector stop failed:", err)
		return err
	}

	// Stop companion
	if plugin != nil {
		if err := plugin.StopCompanion(ws); err != nil {
			fmt.Println(" companion stop failed:", err)
			return err
		}
	}

	fmt.Println(" stopped")
	return nil
}

func runOtelStatus(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	// Resolve active environment for status context
	envName := viper.GetString("environment")
	if envName == "" {
		envName = "local"
	}
	env, ok := ws.ResolveEnvironment(envName)
	if !ok {
		env, _ = ws.ResolveEnvironment("local")
		envName = "local"
	}

	plugin := activePlugin(ws, env)
	pluginName := "unknown"
	if plugin != nil {
		pluginName = plugin.Name()
	}

	collectorRunning := otel.CollectorRunning(ws)
	companionRunning := plugin != nil && plugin.CompanionRunning(ws)

	fmt.Printf("OTEL status for '%s':\n", ws.Name)

	// Show plugin name with env context when env drives forwarding
	if env.Observability.OTLPEndpoint != "" && ws.OtelPlugin != "signoz" {
		fmt.Printf("  plugin:     %s (from environment: %s)\n", pluginName, envName)
		fmt.Printf("  upstream:   %s\n", env.Observability.OTLPEndpoint)
	} else {
		fmt.Printf("  plugin:     %s\n", pluginName)
	}

	if collectorRunning {
		fmt.Printf("  collector:  running\n")
	} else {
		fmt.Printf("  collector:  stopped\n")
	}

	if plugin != nil && plugin.Name() != "forwarding" {
		if companionRunning {
			fmt.Printf("  companion:  running\n")
		} else {
			fmt.Printf("  companion:  stopped\n")
		}
	}

	fmt.Printf("  otlp:       grpc=localhost:%d  http=localhost:%d\n", ws.GRPCPort(), ws.HTTPPort())

	if plugin != nil {
		if queryEndpoint := plugin.QueryEndpoint(ws); queryEndpoint != "" {
			fmt.Printf("  ui:         %s\n", queryEndpoint)
		}
		// Forwarding plugin with no upstream configured → debug/local mode
		if plugin.Name() == "forwarding" && env.Observability.OTLPEndpoint == "" && ws.PluginConfig("upstream") == "" {
			fmt.Printf("  mode:       debug (no upstream configured — telemetry logged to collector stdout)\n")
			fmt.Printf("              To forward: devstack otel configure --plugin=forwarding --set upstream=<host:port> --set protocol=grpc\n")
		} else if plugin.Name() == "forwarding" {
			upstream := ws.PluginConfig("upstream")
			if upstream == "" {
				upstream = env.Observability.OTLPEndpoint
			}
			fmt.Printf("  mode:       forwarding → %s\n", upstream)
		}
	}

	// Per-service telemetry evidence — whether signals are actually arriving.
	if statuses, terr := telemetry.Status(ws.Path); terr == nil && len(statuses) > 0 {
		fmt.Printf("\nevidence:\n")
		for _, s := range statuses {
			fmt.Printf("  %s: confidence=%s traces=%d logs=%t collector_reachable=%t mode=%s\n",
				s.Service, s.Confidence, s.TraceCount, s.LogEvidence, s.CollectorReachable, s.Mode)
			fmt.Printf("    %s\n", s.Interpretation)
		}
	}

	if !collectorRunning || !companionRunning {
		fmt.Printf("\nRun: devstack otel start\n")
	}
	return nil
}

func runOtelOpen(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	localEnv, _ := ws.ResolveEnvironment("local")
	plugin := activePlugin(ws, localEnv)
	if plugin == nil {
		return fmt.Errorf("no OTEL plugin active")
	}

	url := plugin.QueryEndpoint(ws)
	if url == "" {
		return fmt.Errorf("plugin '%s' has no local UI endpoint", plugin.Name())
	}

	fmt.Printf("Opening observability UI for '%s': %s\n", ws.Name, url)
	return exec.Command("xdg-open", url).Start()
}

func runOtelConfigure(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	pluginName, _ := cmd.Flags().GetString("plugin")
	setFlags, _ := cmd.Flags().GetStringArray("set")

	// Parse --set key=value pairs
	setConfig := map[string]string{}
	for _, kv := range setFlags {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid --set value %q (expected key=value)", kv)
		}
		setConfig[parts[0]] = parts[1]
	}

	if pluginName == "" && len(setConfig) == 0 {
		return fmt.Errorf("specify --plugin=<name> and/or --set key=value")
	}

	// pluginNameForValidation is used for schema validation only.
	// If no --plugin flag was given we don't change ws.OtelPlugin — preserving ""
	// keeps env-driven plugin selection working.
	pluginNameForValidation := pluginName
	if pluginNameForValidation == "" {
		pluginNameForValidation = ws.OtelPlugin
		if pluginNameForValidation == "" {
			pluginNameForValidation = "forwarding"
		}
	}

	// Validate the plugin exists
	p := otel.Get(pluginNameForValidation)
	if p == nil {
		return fmt.Errorf("unknown plugin %q — run: devstack otel plugins", pluginName)
	}

	// Validate required config fields
	for _, field := range p.ConfigSchema() {
		if field.Required {
			val := setConfig[field.Key]
			if val == "" {
				val = ws.PluginConfig(field.Key)
			}
			if val == "" {
				return fmt.Errorf("plugin %q requires config key %q — use --set %s=<value>", pluginName, field.Key, field.Key)
			}
		}
	}

	// Merge existing config with new values
	merged := map[string]string{}
	for k, v := range ws.OtelPluginConfig {
		merged[k] = v
	}
	for k, v := range setConfig {
		merged[k] = v
	}

	if err := workspace.UpdateOtelPlugin(ws.Name, pluginName, merged); err != nil {
		return fmt.Errorf("failed to update workspace config: %w", err)
	}

	// Persist the backend where it's actually read from. On manifest workspaces
	// that's the manifest (otherwise the manifest shadows the change on reload);
	// legacy workspaces keep using .devstack.json.
	if ws.Path != "" {
		if config.HasWorkspaceManifest(ws.Path) {
			if pluginName != "" {
				if err := config.SetObservabilityBackend(ws.Path, pluginName); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to write backend to manifest: %v\n", err)
				}
			}
		} else if err := saveOtelPluginToProject(ws.Path, pluginName, merged); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save to project config: %v\n", err)
		}
	}

	displayName := pluginName
	if displayName == "" {
		displayName = pluginNameForValidation + " (env-driven)"
	}
	fmt.Printf("Plugin configured: %s\n", displayName)
	for k, v := range setConfig {
		fmt.Printf("  %s = %s\n", k, v)
	}
	fmt.Printf("\nRun: devstack otel start\n")
	return nil
}

func runOtelEnable(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}
	backend, _ := cmd.Flags().GetString("backend")

	if err := config.SetObservabilityEnabled(ws.Path, true); err != nil {
		return err
	}
	if backend != "" {
		if err := config.SetObservabilityBackend(ws.Path, backend); err != nil {
			return err
		}
	}

	effective := backend
	if effective == "" {
		effective = "signoz (default)"
	}
	fmt.Printf("Observability enabled for '%s' (backend: %s)\n", ws.Name, effective)
	fmt.Printf("\nRun: devstack otel start\n")
	return nil
}

func runOtelDisable(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}
	if err := config.SetObservabilityEnabled(ws.Path, false); err != nil {
		return err
	}
	fmt.Printf("Observability disabled for '%s'. No collector will start on 'devstack workspace up'.\n", ws.Name)
	fmt.Printf("Stop a running collector now with: devstack otel stop\n")
	return nil
}

func saveOtelPluginToProject(wsPath, pluginName string, pluginConfig map[string]string) error {
	cfg, err := config.Load(wsPath)
	if err != nil {
		return err
	}
	// Only overwrite plugin name when explicitly set; empty means env-driven
	// selection and the existing project plugin name should be preserved.
	if pluginName != "" {
		cfg.OtelPlugin = pluginName
	}
	cfg.OtelPluginConfig = pluginConfig
	return config.Save(wsPath, cfg)
}

func runOtelPlugins(cmd *cobra.Command, args []string) error {
	// Try to detect workspace for active plugin marker
	var activePluginName string
	if ws, err := workspace.DetectFromCwd(); err == nil {
		localEnv, _ := ws.ResolveEnvironment("local")
		p := activePlugin(ws, localEnv)
		if p != nil {
			activePluginName = p.Name()
		}
	}

	plugins := otel.All()
	if len(plugins) == 0 {
		fmt.Println("No plugins registered.")
		return nil
	}

	fmt.Println("Registered OTEL plugins:")
	for _, p := range plugins {
		active := "  "
		if p.Name() == activePluginName {
			active = "* "
		}
		fmt.Printf("%s%s\n", active, p.Name())
		schema := p.ConfigSchema()
		if len(schema) > 0 {
			for _, field := range schema {
				req := ""
				if field.Required {
					req = " (required)"
				}
				def := ""
				if field.Default != "" {
					def = fmt.Sprintf(" [default: %s]", field.Default)
				}
				fmt.Printf("      %-20s %s%s%s\n", field.Key, field.Description, req, def)
			}
		}
	}
	return nil
}
