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
	Short: "Start the MCP server",
	Long:  `Start the devstack MCP server with HTTP or stdio transport.`,
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

	port := viper.GetInt("tilt.port")
	// If port is still the default (10350), try to resolve from the workspace registry.
	// This ensures that once a workspace is registered, the MCP server auto-uses the right port.
	if port == 10350 {
		wsName := viper.GetString("workspace")
		if wsName != "" {
			if ws, err := workspace.FindByName(wsName); err == nil {
				port = ws.TiltPort
			} else if ws, err := workspace.FindByPath(wsName); err == nil {
				port = ws.TiltPort
			}
		}
	}

	tiltClient := tilt.NewClient(
		viper.GetString("tilt.host"),
		port,
	)

	defaultService := viper.GetString("default_service")
	nvxmcp.RegisterTools(mcpServer, tiltClient, defaultService)

	log.Printf("Starting devstack MCP server with stdio transport")

	return server.ServeStdio(mcpServer)
}

func serveHTTP() error {
	log.Printf("HTTP transport not yet implemented")
	return fmt.Errorf("HTTP transport not yet implemented")
}
