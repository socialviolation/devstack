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
	Long: `Start the devstack MCP (Model Context Protocol) server. The server exposes
devstack capabilities as tools that an AI agent can call directly.

'devstack init' writes this configuration into each service's .mcp.json. You do
not normally run this command by hand.

TOOLS EXPOSED TO AI AGENTS
  environment     which workspace, which copies, which tools (start here)
  status          live state of every copy: state, port, path, branch, env
  topology        the declared service graph: groups, deps, callers
  start           start a service or a group, dependencies first
  restart         rebuild and restart a service or a group
  stop            stop one service, a group, or every service of one copy (all=true)
  process_logs    fetch stdout/stderr from a running copy
  service_env     read and write a service's env files, and audit them
  base            print the replica that base runs from, or sync it to the default branch tip
  configure       read or set a dev daemon runtime argument
  hooks           list the declared lifecycle hooks, or run the hooks of one event
  migrate         list the migrations of this machine, or run the pending ones
  observability   show, enable or disable OTEL, and read the telemetry evidence
  stack_create    cut a feature stack from the default branch
  stack_add       put another service into a stack that already exists
  stack_list      the stacks in flight, with their overlays, links and notes
  stack_note      read, set or append to what a stack is for
  stack_up        start a stack in the host daemon
  stack_down      stop a stack's copies, and keep its worktrees
  stack_rm        tear a stack down: worktrees, ports and record
  env_use         point a scope at a named env
  env_which       the env a service resolves to, rung by rung
  env_set         set a variable on a named env
  investigate     correlated traces + logs in one call        [when observability enabled]
  tunnel          forward local service ports to/from a remote over SSH [when an ssh client is available]

The tool set adapts to the active workspace. The telemetry tools appear only when
the manifest enables observability. The tunnel tools appear only when an ssh
client is available. Call the 'environment' tool first to see what is available.

The tools do not cover every command. These are shell only: workspace up and down,
ports, dependencies, group add and remove, env list, show and remove, stack config,
and init.

TRANSPORT
  stdio (default)   Claude Code and most AI tooling use this
  http              for a custom integration`,
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
		return fmt.Errorf("invalid transport mode: %s. It must be 'http' or 'stdio'", transport)
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
		log.Printf("observability queries are unavailable: %v", err)
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
		patches(),
	)

	log.Printf("Starting devstack MCP server (workspace: %s, tilt-port: %d)", ws.Name, workspace.HostTiltPort)

	return server.ServeStdio(mcpServer)
}

func serveHTTP() error {
	log.Printf("the HTTP transport is not implemented yet")
	return fmt.Errorf("the HTTP transport is not implemented yet")
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
		log.Printf("Warning: workspace %q is not in the registry. devstack detects the workspace from the current directory instead", nameOrPath)
	}
	ws, err := resolveWorkspace("")
	if err != nil {
		log.Fatalf("Can not resolve the workspace. DEVSTACK_WORKSPACE=%q, and detection from the current directory failed: %v\nRun 'devstack workspace add' to register this workspace.", nameOrPath, err)
	}
	return ws
}
