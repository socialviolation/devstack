package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
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
	Long: `devstack runs one otelcol-contrib collector and one telemetry backend for the
whole machine. Every workspace shares them. Every service that 'devstack init'
registered sends OpenTelemetry traces and logs to the collector through
OTEL_EXPORTER_OTLP_ENDPOINT, on gRPC port 4317. devstack stamps each signal with
its workspace, its stack and its service name. You can then slice the telemetry
at query time.

The default backend is OpenObserve. It is one container, and it needs no
configuration. Each workspace can choose its own backend, and the collector routes
between them:

  devstack otel config set --plugin=forwarding --set upstream=<host:port> --set protocol=grpc
  devstack otel config set --plugin=signoz      # heavier: a local SigNoz in Docker

'devstack workspace up' starts the collector for you.

'devstack otel config' writes this workspace's manifest, and it touches no
process. 'devstack otel start' and 'devstack otel stop' run and kill the
collector, and they change no configuration.

SUBCOMMANDS
  CONFIG — writes devstack.workspace.yaml, starts and stops nothing
  devstack otel config on          record that this workspace wants observability
  devstack otel config off         record that it does not
  devstack otel config set         choose the backend plugin and its settings

  PROCESS — runs and kills the collector, changes no configuration
  devstack otel start              start the collector and its companion now
  devstack otel stop               stop the collector and its companion now

  READ
  devstack otel status             collector state, ports, and per-service telemetry evidence
  devstack otel open               open the observability UI in the browser
  devstack otel traces             query traces from the configured backend
  devstack otel logs               query the collected logs
  devstack otel services           list the services that report telemetry
  devstack otel plugins            list every registered plugin`,
}

var otelConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Change this workspace's observability configuration (it starts and stops nothing)",
	Long: `Read and write the observability settings in this workspace's manifest.

Every subcommand here edits devstack.workspace.yaml and nothing else. It starts
no collector, and it stops none. To run or to kill the collector itself, use
'devstack otel start' and 'devstack otel stop'.`,
}

var otelStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the collector process now (changes no configuration)",
	Long: `Start the local otelcol-contrib collector and its companion for this workspace.

This command runs a process. It changes no configuration. If the manifest of a
workspace says that observability is off, it stays off on the next
'devstack workspace up'. To turn it on, run 'devstack otel config on'.

The active plugin controls what the collector does with the telemetry. To change
the plugin, run 'devstack otel config set'.

'devstack workspace up' runs this command for you.`,
	RunE: runOtelStart,
}

var otelStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the collector process now (changes no configuration)",
	Long: `Stop the collector and the companion that run for this workspace.

This command kills a process. It does not touch the workspace's manifest, so the
collector starts again on the next 'devstack workspace up'. To stop that, run
'devstack otel config off'.`,
	RunE: runOtelStop,
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

var otelConfigSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set the backend plugin and its settings in the workspace manifest",
	Long: `Choose the active OTEL plugin for this workspace, and set its settings.

devstack writes the --plugin value (the backend) and the --set values to the
observability block of the workspace manifest. They persist, and they travel with
the project. That file is committed. devstack therefore keeps credential keys out
of it, and stores api_key, tokens and passwords in the machine-local registry.

This command writes configuration only. A running collector keeps its old
settings. To apply the new ones, run 'devstack otel stop', then
'devstack otel start'.

Examples:
  devstack otel config set --plugin=openobserve
  devstack otel config set --plugin=forwarding --set upstream=https://otel.example.com:4318 --set deployment_env=dev`,
	RunE: runOtelConfigSet,
}

var otelConfigOnCmd = &cobra.Command{
	Use:   "on",
	Short: "Record that this workspace wants observability (writes configuration, starts nothing)",
	Long: `Write observability.enabled: true to the workspace manifest. Then
'devstack workspace up' starts a collector, and devstack pushes the OTEL export
variables down to the services.

This command starts nothing now. To start the collector now, run
'devstack otel start'.

Examples:
  devstack otel config on
  devstack otel config on --backend=forwarding`,
	RunE: runOtelConfigOn,
}

var otelConfigOffCmd = &cobra.Command{
	Use:   "off",
	Short: "Record that this workspace does not want observability (writes configuration, stops nothing)",
	Long: `Write observability.enabled: false to the workspace manifest. Then
'devstack workspace up' starts no collector, and devstack does not assume that
the services carry OTEL instrumentation.

This command stops nothing now. A collector that is already running keeps running
until you run 'devstack otel stop'.`,
	RunE: runOtelConfigOff,
}

var otelPluginsCmd = &cobra.Command{
	Use:   "plugins",
	Short: "List every registered OTEL plugin",
	RunE:  runOtelPlugins,
}

func init() {
	rootCmd.AddCommand(otelCmd)
	otelCmd.AddCommand(otelConfigCmd)
	otelConfigCmd.AddCommand(otelConfigOnCmd)
	otelConfigCmd.AddCommand(otelConfigOffCmd)
	otelConfigCmd.AddCommand(otelConfigSetCmd)
	otelCmd.AddCommand(otelStartCmd)
	otelCmd.AddCommand(otelStopCmd)
	otelCmd.AddCommand(otelStatusCmd)
	otelCmd.AddCommand(otelOpenCmd)
	otelCmd.AddCommand(otelPluginsCmd)

	for _, sub := range []*cobra.Command{otelConfigOnCmd, otelConfigOffCmd, otelStartCmd, otelStopCmd, otelStatusCmd, otelOpenCmd, otelConfigSetCmd} {
		sub.Flags().String("workspace", "", "Workspace name or path. Default: the workspace of the current directory (env: DEVSTACK_WORKSPACE)")
	}
	otelConfigOnCmd.Flags().String("backend", "", "Observability backend to use. Default: openobserve")

	otelConfigSetCmd.Flags().String("plugin", "", "Plugin name to make active (for example openobserve, signoz, forwarding)")
	otelConfigSetCmd.Flags().StringArray("set", nil, "Set one plugin configuration key, as key=value. Repeat the flag for more")
}

// resolveOtelWorkspace reads the local --workspace flag of an otel subcommand.
// That flag shadows the persistent one of the root command, which is the flag
// viper binds to DEVSTACK_WORKSPACE, so the viper key is the fallback that makes
// the environment variable apply here too.
func resolveOtelWorkspace(cmd *cobra.Command) (*workspace.Workspace, error) {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	if wsFlag == "" {
		wsFlag = viper.GetString("workspace")
	}
	var (
		ws  *workspace.Workspace
		err error
	)
	ws, err = resolveWorkspace(wsFlag)
	if err != nil {
		return nil, fmt.Errorf("devstack can not detect the workspace: %w\nTry: devstack otel <subcommand> --workspace=<name>", err)
	}
	ws.OverlayProjectConfig()
	return ws, nil
}

func isOtelRunning(ws *workspace.Workspace) bool {
	// CompanionRunning for forwarding always returns true; backends with local
	// infrastructure check their container.
	plugin := activePlugin(ws)
	if plugin == nil {
		return false
	}
	return otel.CollectorRunning() && plugin.CompanionRunning(ws)
}

func runOtelStart(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	plugin := activePlugin(ws)
	if plugin == nil {
		return fmt.Errorf("no OTEL plugin is registered. This is a bug in devstack")
	}

	// A stale companion is replaced even when everything is up: this is the
	// command that applies a devstack upgrade.
	if isOtelRunning(ws) && !plugin.CompanionStale(ws) {
		queryEndpoint := plugin.QueryEndpoint(ws)
		if queryEndpoint != "" {
			fmt.Printf("The collector and its companion already run for '%s' (plugin: %s) — %s\n", ws.Name, plugin.Name(), queryEndpoint)
		} else {
			fmt.Printf("The collector and its companion already run for '%s' (plugin: %s)\n", ws.Name, plugin.Name())
		}
		return nil
	}

	// Validate prerequisites
	if err := plugin.Validate(ws); err != nil {
		return fmt.Errorf("the plugin check failed: %w", err)
	}

	fmt.Printf("devstack starts the collector and its companion for '%s' (plugin: %s)...\n", ws.Name, plugin.Name())

	if err := startOtelStack(ws, plugin); err != nil {
		return err
	}

	queryEndpoint := plugin.QueryEndpoint(ws)
	fmt.Printf("  plugin:   %s\n", plugin.Name())
	fmt.Printf("  otlp:     http://localhost:%d (HTTP)\n", workspace.OTLPHTTPPort)
	fmt.Printf("  grpc:     localhost:%d\n", workspace.OTLPGRPCPort)
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

	plugin := activePlugin(ws)

	collectorUp := otel.CollectorRunning()
	companionUp := plugin != nil && plugin.CompanionRunning(ws)
	if !collectorUp && !companionUp {
		fmt.Printf("The collector and its companion do not run for '%s'\n", ws.Name)
		return nil
	}

	fmt.Printf("devstack stops the collector and its companion for '%s'...", ws.Name)

	if err := stopOtelStack(ws, plugin); err != nil {
		fmt.Println(" failed:", err)
		return err
	}

	fmt.Println(" stopped")
	return nil
}

func runOtelStatus(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	plugin := activePlugin(ws)
	pluginName := "unknown"
	if plugin != nil {
		pluginName = plugin.Name()
	}

	collectorRunning := otel.CollectorRunning()
	companionRunning := plugin != nil && plugin.CompanionRunning(ws)

	fmt.Printf("OTEL status for '%s':\n", ws.Name)

	fmt.Printf("  plugin:     %s\n", pluginName)

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

	fmt.Printf("  otlp:       grpc=localhost:%d  http=localhost:%d\n", workspace.OTLPGRPCPort, workspace.OTLPHTTPPort)

	if plugin != nil {
		if queryEndpoint := plugin.QueryEndpoint(ws); queryEndpoint != "" {
			fmt.Printf("  ui:         %s\n", queryEndpoint)
		}
		// Forwarding plugin with no upstream configured → debug/local mode
		if plugin.Name() == "forwarding" && ws.PluginConfig("upstream") == "" {
			fmt.Printf("  mode:       debug (no upstream configured — the collector writes the telemetry to its stdout)\n")
			fmt.Printf("              To forward: devstack otel config set --plugin=forwarding --set upstream=<host:port> --set protocol=grpc\n")
		} else if plugin.Name() == "forwarding" {
			fmt.Printf("  mode:       forwarding → %s\n", ws.PluginConfig("upstream"))
		}
	}

	// Per-variant telemetry evidence — which instances are emitting,
	// queried from the backend rather than inferred.
	evidenceBackend, _ := otel.BackendFor(ws)
	if statuses, terr := telemetry.Status(ws.Path, evidenceBackend, telemetry.DefaultWindow); terr == nil && len(statuses) > 0 {
		fmt.Printf("\nevidence (last %s):\n", telemetry.DefaultWindow)
		for _, s := range statuses {
			fmt.Printf("  %s: confidence=%s spans=%d mode=%s\n", s.Service, s.Confidence, s.TraceCount, s.Mode)
			fmt.Printf("    %s\n", s.Summary())
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

	plugin := activePlugin(ws)
	if plugin == nil {
		return fmt.Errorf("no OTEL plugin is active for this workspace")
	}

	url := plugin.QueryEndpoint(ws)
	if url == "" {
		return fmt.Errorf("plugin '%s' has no local UI to open", plugin.Name())
	}

	fmt.Printf("devstack opens the observability UI for '%s': %s\n", ws.Name, url)
	return exec.Command("xdg-open", url).Start()
}

func runOtelConfigSet(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	pluginName, _ := cmd.Flags().GetString("plugin")
	setFlags, _ := cmd.Flags().GetStringArray("set")

	setConfig, err := parseOtelSetFlags(setFlags)
	if err != nil {
		return err
	}

	if pluginName == "" && len(setConfig) == 0 {
		return fmt.Errorf("pass --plugin=<name>, or --set key=value, or both")
	}

	// pluginNameForValidation is used for schema validation only. With no --plugin
	// flag we validate against the workspace's current plugin and leave it unchanged.
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
				return fmt.Errorf("plugin %q needs the configuration key %q. Pass --set %s=<value>", pluginName, field.Key, field.Key)
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
		return fmt.Errorf("devstack did not update the workspace configuration: %w", err)
	}

	// Persist to the manifest, which is where the settings are read back from —
	// otherwise it shadows the registry write above on the next reload.
	if ws.Path != "" && config.HasWorkspaceManifest(ws.Path) {
		if pluginName != "" {
			if err := config.SetObservabilityBackend(ws.Path, pluginName); err != nil {
				fmt.Fprintf(os.Stderr, "warning: devstack did not write the backend to the manifest: %v\n", err)
			}
		}
		// Credentials stay out of the manifest — it is a committed file. They are
		// already persisted to the machine-local registry above.
		manifestSettings := map[string]string{}
		var heldBack []string
		for k, v := range setConfig {
			if config.IsCredentialKey(k) {
				heldBack = append(heldBack, k)
				continue
			}
			manifestSettings[k] = v
		}
		if len(manifestSettings) > 0 {
			if err := config.SetObservabilitySettings(ws.Path, manifestSettings); err != nil {
				fmt.Fprintf(os.Stderr, "warning: devstack did not write the settings to the manifest: %v\n", err)
			}
		}
		if len(heldBack) > 0 {
			sort.Strings(heldBack)
			fmt.Printf("devstack stored %s in the machine-local registry, and not in %s. That file is committed.\n",
				strings.Join(heldBack, ", "), config.WorkspaceManifestFileName)
		}
	}

	displayName := pluginName
	if displayName == "" {
		displayName = pluginNameForValidation
	}
	fmt.Printf("Plugin configured: %s\n", displayName)
	for k, v := range setConfig {
		fmt.Printf("  %s = %s\n", k, v)
	}
	fmt.Printf("\nA running collector keeps its old settings. To apply the new ones, run: devstack otel stop, then devstack otel start\n")
	return nil
}

// parseOtelSetFlags turns --set key=value pairs into a config map. Credential
// keys are refused: these settings are persisted to the workspace manifest,
// which is committed.
func parseOtelSetFlags(setFlags []string) (map[string]string, error) {
	setConfig := map[string]string{}
	for _, kv := range setFlags {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("devstack did not read the --set value %q as key=value", kv)
		}
		setConfig[parts[0]] = parts[1]
	}
	return setConfig, nil
}

func runOtelConfigOn(cmd *cobra.Command, args []string) error {
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
		effective = config.DefaultObservabilityBackend + " (default)"
	}
	fmt.Printf("devstack turned observability on in the configuration for '%s' (backend: %s)\n", ws.Name, effective)
	fmt.Printf("\nNothing runs yet. To start the collector, run: devstack otel start\n")
	return nil
}

func runOtelConfigOff(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}
	if err := config.SetObservabilityEnabled(ws.Path, false); err != nil {
		return err
	}
	fmt.Printf("devstack turned observability off in the configuration for '%s'. No collector starts on 'devstack workspace up'.\n", ws.Name)
	fmt.Printf("A collector that runs right now keeps running. To stop it, run: devstack otel stop\n")
	return nil
}

func runOtelPlugins(cmd *cobra.Command, args []string) error {
	// Try to detect workspace for active plugin marker
	var activePluginName string
	if ws, err := resolveWorkspace(""); err == nil {
		p := activePlugin(ws)
		if p != nil {
			activePluginName = p.Name()
		}
	}

	plugins := otel.All()
	if len(plugins) == 0 {
		fmt.Println("No plugins are registered.")
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
