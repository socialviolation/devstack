package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
	"github.com/socialviolation/devstack/internal/otel"
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
  environment     which workspace, which copies, which tools (start here)
  status          live state of every copy: state, port, path, branch, env
  topology        the declared service graph: groups, deps, callers
  start           start a service or a group, dependencies first
  restart         trigger a rebuild and restart of a service or a group
  stop            disable one service, a group, or (with all=true) an instance
  process_logs    fetch stdout/stderr from a running copy
  service_env     read and write a service's env files, and audit them
  base            print the replica base runs from, or sync it to the default branch tip
  configure       read or set a dev daemon runtime argument
  hooks           list the declared lifecycle hooks, or fire an event's by hand
  observability   inspect/enable/disable OTEL + telemetry evidence
  stack_create    cut a feature stack from the default branch
  stack_add       put another service into a stack that already exists
  stack_list      the stacks in flight, with their overlays, links and notes
  stack_note      read, set or append to what a stack is for
  stack_up        bring a stack up in the host daemon
  stack_down      stop a stack's copies, keeping its worktrees
  stack_rm        tear a stack down: worktrees, ports and record
  env_use         point a scope at a named config env
  env_which       the env a service resolves to, rung by rung
  env_set         set a config var on a named env
  investigate     correlated traces + logs in one call        [when observability enabled]
  tunnel          forward local service ports to/from a remote over SSH [when an ssh client is available]

The exact tool set adapts to the active workspace: trace/telemetry tools appear only
when observability is enabled in the manifest, and tunnel tools only when an ssh client is available.
Call the 'environment' tool first to see what's available.

The tools do not cover every command. These are shell only: workspace up and down,
ports, dependencies, group add and remove, env list, show and remove, stack config,
and init.

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

	// Resolved from the workspace's configured plugin — no backend name, URL or
	// credential is ever asked of the caller. A workspace whose telemetry lives
	// somewhere unqueryable (pure forwarding) serves its tools without one.
	backend, err := otel.BackendFor(ws)
	if err != nil {
		log.Printf("observability queries unavailable: %v", err)
	}

	tiltClient := tilt.NewDynamicClient(host, func() int {
		return workspace.HostTiltPort
	})

	nvxmcp.RegisterTools(
		mcpServer,
		tiltClient,
		defaultService,
		backend,
		ws.Name,
		ws.Path,
		ws,
	)

	log.Printf("Starting devstack MCP server (workspace: %s, tilt-port: %d)", ws.Name, workspace.HostTiltPort)

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
	ws, err := resolveWorkspace("")
	if err != nil {
		log.Fatalf("Cannot resolve workspace: DEVSTACK_WORKSPACE=%q and cwd detection failed: %v\nRun 'devstack workspace add' to register this workspace.", nameOrPath, err)
	}
	return ws
}
