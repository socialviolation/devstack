package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
)

var otelCmd = &cobra.Command{
	Use:   "otel",
	Short: "Manage the observability backend (Aspire Dashboard or BYO endpoint)",
}

var otelStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the managed Aspire Dashboard container for a workspace",
	RunE:  runOtelStart,
}

var otelStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the managed Aspire Dashboard container for a workspace",
	RunE:  runOtelStop,
}

var otelStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the Aspire Dashboard container is running",
	RunE:  runOtelStatus,
}

var otelOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the Aspire Dashboard in the browser",
	RunE:  runOtelOpen,
}

var otelSetEndpointCmd = &cobra.Command{
	Use:   "set-endpoint <otlp-url>",
	Short: "Switch to BYO mode with a user-provided OTLP endpoint",
	Long: `Switch the workspace to BYO (bring-your-own) mode.

devstack will configure services to push OTLP telemetry to <otlp-url>
instead of starting a managed Aspire Dashboard container.

Optionally provide --query-url to enable trace queries in MCP tools.

Examples:
  devstack otel set-endpoint http://my-collector:4318
  devstack otel set-endpoint http://my-collector:4318 --query-url=http://my-ui:3000`,
	Args: cobra.ExactArgs(1),
	RunE: runOtelSetEndpoint,
}

var otelManagedCmd = &cobra.Command{
	Use:   "managed",
	Short: "Switch back to managed mode (Aspire Dashboard container)",
	Long:  `Switch the workspace back to managed mode. devstack will start and manage an Aspire Dashboard container for observability.`,
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

	otelSetEndpointCmd.Flags().String("query-url", "", "Optional query API URL for MCP trace tools (e.g. http://my-signoz:3301 or http://my-grafana:3000)")
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

	containerName := workspace.OtelContainerName(ws.Name)

	if isOtelRunning(containerName) {
		fmt.Printf("Aspire Dashboard already running for '%s' — %s\n", ws.Name, otelUIURL)
		return nil
	}

	fmt.Printf("Starting Aspire Dashboard for '%s'...", ws.Name)
	if err := startOtel(containerName); err != nil {
		fmt.Println(" failed")
		return err
	}
	fmt.Printf(" started\n✓ %s\n", otelUIURL)
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

	containerName := workspace.OtelContainerName(ws.Name)

	if !isOtelRunning(containerName) {
		fmt.Printf("Aspire Dashboard is not running for '%s'\n", ws.Name)
		return nil
	}

	fmt.Printf("Stopping Aspire Dashboard for '%s'...", ws.Name)
	if err := stopOtel(containerName); err != nil {
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

	containerName := workspace.OtelContainerName(ws.Name)

	if isOtelRunning(containerName) {
		fmt.Printf("Aspire Dashboard running for '%s': %s\n", ws.Name, otelUIURL)
		fmt.Printf("  OTLP HTTP: http://localhost:%s\n", otelOTLPHTTPPort)
		fmt.Printf("  OTLP gRPC: http://localhost:%s\n", otelOTLPGRPCPort)
	} else {
		fmt.Printf("Aspire Dashboard not running for '%s'\n", ws.Name)
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

	fmt.Printf("Opening Aspire Dashboard for '%s': %s\n", ws.Name, otelUIURL)
	return exec.Command("xdg-open", otelUIURL).Start()
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

	containerName := workspace.OtelContainerName(ws.Name)
	if isOtelRunning(containerName) {
		fmt.Printf("Stopping managed Aspire Dashboard container...")
		if stopErr := stopOtel(containerName); stopErr != nil {
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

	fmt.Printf("Workspace '%s' switched to managed mode (Aspire Dashboard)\n", ws.Name)
	fmt.Printf("Start the container: devstack otel start --workspace=%s\n", ws.Name)
	fmt.Printf("Dashboard UI:        %s\n", otelUIURL)

	return nil
}
