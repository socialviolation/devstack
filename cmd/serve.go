package cmd

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
	"github.com/socialviolation/devstack/internal/observability"
	_ "github.com/socialviolation/devstack/internal/observability/signoz" // register signoz backend
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP server for AI agent tool access",
	Long: `Start the devstack MCP (Model Context Protocol) server, which exposes
devstack capabilities as tools that AI agents can call directly.

This is configured automatically in each service's .mcp.json by 'devstack init'.
You do not normally need to run this manually.

TOOLS EXPOSED TO AI AGENTS
  environment     active environment + capability orientation (start here)
  status          live state of all services (running, error, ports)
  restart         trigger a rebuild and restart of a service  [local only]
  stop            disable one or all services                 [local only]
  process_logs    fetch stdout/stderr from a service          [local only]
  investigate     correlated traces + logs in one call        [when observability enabled]
  observability   inspect/enable/disable OTEL + telemetry evidence [local only]
  configure       set a Tilt runtime argument                 [local only]
  tunnel          forward local service ports to/from a remote over SSH [local, when tailscale installed]

The exact tool set adapts to the active workspace: trace/telemetry tools appear only
when observability is enabled in the manifest, and tunnel tools only when tailscale is installed.
Call the 'environment' tool first to see what's available.

TRANSPORT
  stdio (default)   used by Claude Code and most AI tooling
  http              for custom integrations`,
	RunE: runServe,
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

	defaultService := viper.GetString("default_service")
	ws := resolveServeWorkspace(wsName)

	// Resolve active environment
	envName := viper.GetString("environment")
	if envName == "" {
		envName = "local"
	}
	activeEnv, ok := resolveActiveEnv(ws, envName)
	if !ok {
		log.Fatalf("environment %q not found in workspace %q. Run: devstack env list", envName, ws.Name)
	}
	allEnvs := allEnvironments(ws)

	// Create observability backend
	backend, err := observability.NewBackend(
		activeEnv.Observability.Backend,
		activeEnv.Observability.URL,
		activeEnv.Observability.APIKey,
	)
	if err != nil {
		log.Fatalf("failed to create observability backend: %v", err)
	}

	// Only create Tilt client for local environments
	var tiltClient *tilt.Client
	if activeEnv.Type == workspace.EnvironmentTypeLocal {
		tiltClient = tilt.NewDynamicClient(host, func() int {
			return workspace.HostTiltPort
		})
	}

	tiltfilePath := filepath.Join(ws.Path, "Tiltfile")

	nvxmcp.RegisterTools(
		mcpServer,
		tiltClient,
		defaultService,
		backend,
		tiltfilePath,
		envName,
		activeEnv,
		allEnvs,
		ws.Name,
		ws.Path,
		ws,
	)

	log.Printf("Starting devstack MCP server (workspace: %s, env: %s/%s, tilt-port: %d)", ws.Name, envName, activeEnv.Type, workspace.HostTiltPort)

	return server.ServeStdio(mcpServer)
}

func serveHTTP() error {
	log.Printf("HTTP transport not yet implemented")
	return fmt.Errorf("HTTP transport not yet implemented")
}

// resolveServeWorkspace returns the Workspace for a given name/path string.
// Falls back to cwd detection if nameOrPath is empty or not found.
// Fatals with a clear message if the workspace cannot be resolved.
func resolveServeWorkspace(nameOrPath string) *workspace.Workspace {
	if nameOrPath != "" {
		if ws, err := workspace.FindByName(nameOrPath); err == nil {
			return ws
		}
		if ws, err := workspace.FindByPath(nameOrPath); err == nil {
			return ws
		}
		log.Printf("Warning: workspace %q not found in registry, falling back to cwd detection", nameOrPath)
	}
	ws, err := workspace.DetectFromCwd()
	if err != nil {
		log.Fatalf("Cannot resolve workspace: DEVSTACK_WORKSPACE=%q and cwd detection failed: %v\nRun 'devstack workspace add' to register this workspace.", nameOrPath, err)
	}
	return ws
}
