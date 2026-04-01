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
registered with 'devstack init' is pre-configured to ship OpenTelemetry traces
and logs to this stack via OTEL_EXPORTER_OTLP_ENDPOINT.

This gives AI agents real-time visibility into what is happening across services
during local development. The MCP 'investigate' tool queries this stack to
correlate traces and logs when something goes wrong.

The stack starts automatically when you run 'devstack workspace up'.

SUBCOMMANDS
  devstack otel status    show whether the stack is running and its ports
  devstack otel start     start the SigNoz stack
  devstack otel stop      stop the SigNoz stack
  devstack otel open      open the SigNoz UI in the browser`,
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
	Short: "Show whether the SigNoz stack is running and its ports",
	RunE:  runOtelStatus,
}

var otelOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the SigNoz trace UI in the browser",
	Long: `Open the SigNoz UI for the current workspace. This is where you can browse
distributed traces, logs, and service maps for all locally running services.`,
	RunE: runOtelOpen,
}

func init() {
	rootCmd.AddCommand(otelCmd)
	otelCmd.AddCommand(otelStartCmd)
	otelCmd.AddCommand(otelStopCmd)
	otelCmd.AddCommand(otelStatusCmd)
	otelCmd.AddCommand(otelOpenCmd)

	for _, sub := range []*cobra.Command{otelStartCmd, otelStopCmd, otelStatusCmd, otelOpenCmd} {
		sub.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	}

	// Port flags — stored in workspace config so they persist.
	otelStartCmd.Flags().Int("ui-port", 0, "SigNoz UI + query API port (default 3301)")
	otelStartCmd.Flags().Int("otlp-grpc-port", 0, "OTLP gRPC ingestion port (default 4317)")
	otelStartCmd.Flags().Int("otlp-http-port", 0, "OTLP HTTP ingestion port (default 4318)")
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

	if isOtelRunning(ws.Name) {
		fmt.Printf("SigNoz already running for '%s' — %s\n", ws.Name, workspace.OtelQueryEndpoint(ws))
		return nil
	}

	fmt.Printf("Starting SigNoz for '%s'...", ws.Name)
	if err := startOtel(ws); err != nil {
		fmt.Println(" failed")
		return err
	}
	fmt.Printf(" started\n  ui:   %s\n  otlp: %s\n  grpc: localhost:%d\n",
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

	if isOtelRunning(ws.Name) {
		fmt.Printf("SigNoz running for '%s'\n", ws.Name)
		fmt.Printf("  ui:   %s\n", workspace.OtelQueryEndpoint(ws))
		fmt.Printf("  otlp: %s\n", workspace.OtelOTLPEndpoint(ws))
		fmt.Printf("  grpc: localhost:%d\n", ws.GRPCPort())
	} else {
		fmt.Printf("SigNoz not running for '%s'\n", ws.Name)
		fmt.Printf("Run: devstack otel start\n")
	}
	return nil
}

func runOtelOpen(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	url := workspace.OtelQueryEndpoint(ws)
	fmt.Printf("Opening SigNoz for '%s': %s\n", ws.Name, url)
	return exec.Command("xdg-open", url).Start()
}

