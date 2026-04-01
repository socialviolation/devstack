package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
)

var otelCmd = &cobra.Command{
	Use:   "otel",
	Short: "Manage the local observability stack (traces and logs)",
	Long: `devstack runs a local SigNoz observability stack per workspace. Every service
onboarded with 'devstack onboard' is pre-configured to ship OpenTelemetry traces
and logs to this stack via OTEL_EXPORTER_OTLP_ENDPOINT.

This gives AI agents real-time visibility into what is happening across services
during local development. The MCP 'investigate' tool queries this stack to
correlate traces and logs when something goes wrong.

The stack starts automatically when you run 'devstack workspace up'. Use these
subcommands to manage it independently or switch to a custom endpoint.

MODES
  managed (default)
    devstack runs a SigNoz container stack locally. Traces and logs are stored
    on this machine and queryable via the SigNoz UI and MCP tools.

  byo (bring your own)
    Point devstack at an existing OTLP endpoint (e.g. a shared staging collector).
    devstack will not start any containers; services will push telemetry there.

SUBCOMMANDS
  devstack otel status          show which mode is active and whether it is running
  devstack otel start           start the managed SigNoz stack
  devstack otel stop            stop the managed SigNoz stack
  devstack otel open            open the SigNoz UI in the browser
  devstack otel set-endpoint    switch to BYO mode with a custom OTLP endpoint
  devstack otel managed         switch back to managed SigNoz mode`,
}

var otelStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the managed SigNoz observability stack",
	Long: `Start the local SigNoz container stack for the current workspace.

SigNoz provides a trace and log UI, and exposes a query API that the devstack
MCP tools use to surface correlated observability data to AI agents.

This is called automatically by 'devstack workspace up'.`,
	RunE: runOtelStart,
}

var otelStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the managed SigNoz observability stack",
	RunE:  runOtelStop,
}

var otelStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the observability stack mode and running state",
	RunE:  runOtelStatus,
}

var otelOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the SigNoz trace UI in the browser",
	Long: `Open the SigNoz UI for the current workspace. This is where you can browse
distributed traces, logs, and service maps for all locally running services.`,
	RunE: runOtelOpen,
}

var otelSetEndpointCmd = &cobra.Command{
	Use:   "set-endpoint <otlp-url>",
	Short: "Switch to BYO mode — push traces to a custom OTLP endpoint",
	Long: `Switch the workspace to BYO (bring-your-own) mode.

devstack will configure services to push OTLP telemetry to your endpoint
instead of running a local SigNoz container stack.

Provide --query-url if your endpoint also exposes a SigNoz-compatible query API,
which enables the devstack MCP trace tools for AI agents.

Examples:
  devstack otel set-endpoint http://my-collector:4318
  devstack otel set-endpoint http://my-collector:4318 --query-url=http://my-signoz:8080`,
	Args: cobra.ExactArgs(1),
	RunE: runOtelSetEndpoint,
}

var otelManagedCmd = &cobra.Command{
	Use:   "managed",
	Short: "Switch back to managed mode (local SigNoz container stack)",
	Long:  `Switch the workspace back to managed mode. devstack will start and manage a local SigNoz container stack. Run 'devstack otel start' after switching.`,
	RunE:  runOtelManaged,
}

func init() {
	rootCmd.AddCommand(otelCmd)
	otelCmd.AddCommand(otelStartCmd)
	otelCmd.AddCommand(otelStopCmd)
	otelCmd.AddCommand(otelStatusCmd)
	otelCmd.AddCommand(otelOpenCmd)
	otelCmd.AddCommand(otelSetEndpointCmd)
	otelCmd.AddCommand(otelManagedCmd)

	for _, sub := range []*cobra.Command{otelStartCmd, otelStopCmd, otelStatusCmd, otelOpenCmd, otelSetEndpointCmd, otelManagedCmd} {
		sub.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	}

	// Port flags for managed mode — stored in workspace config so they persist.
	otelStartCmd.Flags().Int("ui-port", 0, "SigNoz UI + query API port (default 8080)")
	otelStartCmd.Flags().Int("otlp-grpc-port", 0, "OTLP gRPC ingestion port (default 4317)")
	otelStartCmd.Flags().Int("otlp-http-port", 0, "OTLP HTTP ingestion port (default 4318)")

	otelSetEndpointCmd.Flags().String("query-url", "", "Optional query API URL for MCP trace tools (e.g. http://my-signoz:8080)")
}

func resolveOtelWorkspace(cmd *cobra.Command) (*workspace.Workspace, error) {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	if wsFlag != "" {
		return resolveWorkspace(wsFlag)
	}
	ws, err := workspace.DetectFromCwd()
	if err != nil {
		return nil, fmt.Errorf("could not detect workspace: %w\nTry: devstack otel <subcommand> --workspace=<name>", err)
	}
	return ws, nil
}

func runOtelStart(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	if ws.OtelMode == "byo" {
		fmt.Printf("Workspace '%s' is in BYO mode — no managed container to start.\n", ws.Name)
		fmt.Printf("OTLP endpoint: %s\n", ws.OtelEndpoint)
		fmt.Printf("To switch back: devstack otel managed --workspace=%s\n", ws.Name)
		return nil
	}

	// Apply any port overrides from flags before starting.
	uiPort, _ := cmd.Flags().GetInt("ui-port")
	grpcPort, _ := cmd.Flags().GetInt("otlp-grpc-port")
	httpPort, _ := cmd.Flags().GetInt("otlp-http-port")
	if uiPort > 0 || grpcPort > 0 || httpPort > 0 {
		if err := workspace.UpdateOtelPorts(ws.Name, uiPort, grpcPort, httpPort); err != nil {
			return fmt.Errorf("failed to save port config: %w", err)
		}
		// Reload so ws has the updated ports.
		ws, err = resolveOtelWorkspace(cmd)
		if err != nil {
			return err
		}
	}

	if isOtelRunning(ws.Name) {
		fmt.Printf("SigNoz already running for '%s' — %s\n", ws.Name, workspace.OtelQueryEndpoint(ws))
		return nil
	}

	fmt.Printf("Starting SigNoz for '%s'...", ws.Name)
	if err := startOtel(ws); err != nil {
		fmt.Println(" failed")
		return err
	}
	fmt.Printf(" started\n✓ UI + Query API: %s\n  OTLP HTTP: %s\n  OTLP gRPC: localhost:%d\n",
		workspace.OtelQueryEndpoint(ws),
		workspace.OtelOTLPEndpoint(ws),
		ws.GRPCPort(),
	)
	return nil
}

func runOtelStop(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	if ws.OtelMode == "byo" {
		fmt.Printf("Workspace '%s' is in BYO mode — no managed container to stop.\n", ws.Name)
		return nil
	}

	if !isOtelRunning(ws.Name) {
		fmt.Printf("SigNoz is not running for '%s'\n", ws.Name)
		return nil
	}

	fmt.Printf("Stopping SigNoz for '%s'...", ws.Name)
	if err := stopOtel(ws.Name); err != nil {
		fmt.Println(" failed")
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

	if ws.OtelMode == "byo" {
		fmt.Printf("Workspace '%s' is in BYO mode\n", ws.Name)
		fmt.Printf("  OTLP endpoint: %s\n", ws.OtelEndpoint)
		if ws.OtelQueryURL != "" {
			fmt.Printf("  Query URL:     %s\n", ws.OtelQueryURL)
		} else {
			fmt.Printf("  Query URL:     (not set — trace MCP tools unavailable)\n")
		}
		return nil
	}

	if isOtelRunning(ws.Name) {
		fmt.Printf("SigNoz running for '%s':\n", ws.Name)
		fmt.Printf("  UI + Query API: %s\n", workspace.OtelQueryEndpoint(ws))
		fmt.Printf("  OTLP HTTP:      %s\n", workspace.OtelOTLPEndpoint(ws))
		fmt.Printf("  OTLP gRPC:      localhost:%d\n", ws.GRPCPort())
	} else {
		fmt.Printf("SigNoz not running for '%s'\n", ws.Name)
		fmt.Printf("Run: devstack otel start --workspace=%s\n", ws.Name)
	}
	return nil
}

func runOtelOpen(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	if ws.OtelMode == "byo" && ws.OtelQueryURL != "" {
		fmt.Printf("Opening BYO query UI for '%s': %s\n", ws.Name, ws.OtelQueryURL)
		return exec.Command("xdg-open", ws.OtelQueryURL).Start()
	}

	url := workspace.OtelQueryEndpoint(ws)
	fmt.Printf("Opening SigNoz for '%s': %s\n", ws.Name, url)
	return exec.Command("xdg-open", url).Start()
}

func runOtelSetEndpoint(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	otlpEndpoint := args[0]
	queryURL, _ := cmd.Flags().GetString("query-url")

	if err := workspace.UpdateOtelBYO(ws.Name, otlpEndpoint, queryURL); err != nil {
		return fmt.Errorf("failed to update workspace config: %w", err)
	}

	if isOtelRunning(ws.Name) {
		fmt.Printf("Stopping managed SigNoz stack...")
		if stopErr := stopOtel(ws.Name); stopErr != nil {
			fmt.Printf(" failed (non-fatal): %v\n", stopErr)
		} else {
			fmt.Println(" stopped")
		}
	}

	fmt.Printf("Workspace '%s' switched to BYO mode\n", ws.Name)
	fmt.Printf("  OTLP endpoint: %s\n", otlpEndpoint)
	if queryURL != "" {
		fmt.Printf("  Query URL:     %s\n", queryURL)
	} else {
		fmt.Printf("  Query URL:     (not set — trace MCP tools will be unavailable)\n")
		fmt.Printf("  Set with: devstack otel set-endpoint %s --query-url=<url> --workspace=%s\n", otlpEndpoint, ws.Name)
	}
	fmt.Printf("\nServices onboarded from now on will use OTEL_EXPORTER_OTLP_ENDPOINT=%s\n", otlpEndpoint)

	return nil
}

func runOtelManaged(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	if ws.OtelMode != "byo" {
		fmt.Printf("Workspace '%s' is already in managed mode.\n", ws.Name)
		return nil
	}

	if err := workspace.UpdateOtelManaged(ws.Name); err != nil {
		return fmt.Errorf("failed to update workspace config: %w", err)
	}

	ws, _ = resolveOtelWorkspace(cmd)
	fmt.Printf("Workspace '%s' switched to managed mode (SigNoz)\n", ws.Name)
	fmt.Printf("Start the stack: devstack otel start --workspace=%s\n", ws.Name)
	fmt.Printf("UI:              %s\n", workspace.OtelQueryEndpoint(ws))

	return nil
}
