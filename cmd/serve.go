package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	nvxmcp "devstack/internal/mcp"
	"devstack/internal/tilt"
	"devstack/internal/workspace"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP server for AI agent tool access",
	Long: `Start the devstack MCP (Model Context Protocol) server, which exposes
devstack capabilities as tools that AI agents can call directly.

This is configured automatically in each service's .mcp.json by 'devstack onboard'
or 'devstack init'. You do not normally need to run this manually.

TOOLS EXPOSED TO AI AGENTS
  status          live state of all services (running, error, ports)
  restart         trigger a rebuild and restart of a service
  stop            disable a running service
  process_logs    fetch stdout/stderr from a service (filter to errors only)
  investigate     correlated traces + logs in one call — use this first when
                  something is broken during feature development
  traces          query recent distributed traces from SigNoz
  errors          surface recent error-level spans across all services
  configure       set a runtime config value for a service

TRANSPORT
  stdio (default)   used by Claude Code and most AI tooling
  http              for custom integrations`,
	RunE:  runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("transport", "stdio", "Transport mode: 'http' or 'stdio' (default: stdio)")
}

func runServe(cmd *cobra.Command, args []string) error {
	// Get transport mode
	transport, _ := cmd.Flags().GetString("transport")
	transport = strings.ToLower(transport)

	if transport != "http" && transport != "stdio" {
		return fmt.Errorf("invalid transport mode: %s (must be 'http' or 'stdio')", transport)
	}

	// For stdio transport, redirect logs to stderr to avoid polluting the transport
	if transport == "stdio" {
		log.SetOutput(os.Stderr)
	}

	if transport == "stdio" {
		return serveStdio()
	}

	return serveHTTP()
}

func serveStdio() error {
	mcpServer := server.NewMCPServer(
		"devstack",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	wsName := viper.GetString("workspace")
	host := viper.GetString("tilt.host")

	tiltClient := tilt.NewDynamicClient(host, func() int {
		if port := workspace.ResolvePort(wsName); port != 0 {
			return port
		}
		return viper.GetInt("tilt.port")
	})

	defaultService := viper.GetString("default_service")

	// Resolve the otel query URL from workspace config (supports BYO mode).
	otelQueryURL := workspace.OtelQueryEndpoint(resolveServeWorkspace(wsName))
	nvxmcp.RegisterTools(mcpServer, tiltClient, defaultService, otelQueryURL)

	log.Printf("Starting devstack MCP server with stdio transport")

	return server.ServeStdio(mcpServer)
}

func serveHTTP() error {
	log.Printf("HTTP transport not yet implemented")
	return fmt.Errorf("HTTP transport not yet implemented")
}

// resolveServeWorkspace returns the Workspace for a given name/path string,
// falling back to a zero-value workspace if not found (e.g. before registration).
func resolveServeWorkspace(nameOrPath string) *workspace.Workspace {
	if ws, err := workspace.FindByName(nameOrPath); err == nil {
		return ws
	}
	if ws, err := workspace.FindByPath(nameOrPath); err == nil {
		return ws
	}
	return &workspace.Workspace{}
}
