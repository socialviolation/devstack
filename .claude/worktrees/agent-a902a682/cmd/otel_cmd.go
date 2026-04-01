package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
)

var otelCmd = &cobra.Command{
	Use:   "otel",
	Short: "Manage the Jaeger OTEL container",
}

var otelStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Jaeger container for a workspace",
	RunE:  runOtelStart,
}

var otelStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Jaeger container for a workspace",
	RunE:  runOtelStop,
}

var otelStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the Jaeger container is running",
	RunE:  runOtelStatus,
}

var otelOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the Jaeger in the browser",
	RunE:  runOtelOpen,
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

	containerName := workspace.OtelContainerName(ws.Name)

	if isOtelRunning(containerName) {
		fmt.Printf("Jaeger already running for '%s' — %s\n", ws.Name, otelUIURL)
		return nil
	}

	fmt.Printf("Starting Jaeger for '%s'...", ws.Name)
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

	containerName := workspace.OtelContainerName(ws.Name)

	if !isOtelRunning(containerName) {
		fmt.Printf("Jaeger is not running for '%s'\n", ws.Name)
		return nil
	}

	fmt.Printf("Stopping Jaeger for '%s'...", ws.Name)
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

	containerName := workspace.OtelContainerName(ws.Name)

	if isOtelRunning(containerName) {
		fmt.Printf("Jaeger running for '%s': %s\n", ws.Name, otelUIURL)
	} else {
		fmt.Printf("Jaeger not running for '%s'\n", ws.Name)
		fmt.Printf("Run: devstack otel start --workspace=%s\n", ws.Name)
	}
	return nil
}

func runOtelOpen(cmd *cobra.Command, args []string) error {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return err
	}

	fmt.Printf("Opening Jaeger for '%s': %s\n", ws.Name, otelUIURL)
	return exec.Command("xdg-open", otelUIURL).Start()
}
