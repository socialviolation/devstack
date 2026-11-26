package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

var serveCmd = &cobra.Command{
	Use:    "serve",
	Hidden: true,
	Short:  "Start the MCP server for AI agent tool access",
	Long: `Start the devstack MCP (Model Context Protocol) server. The server exposes
devstack capabilities as tools that an AI agent can call directly.

'devstack init' writes this configuration into each service's .mcp.json. You do
not normally run this command by hand.

TOOLS EXPOSED TO AI AGENTS
  environment     which workspace, which copies, which tools (start here)
  status          live state of every copy: state, port, path, branch, env
  urls            the tailnet address that reaches each copy from another machine
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
  stack_config    the configuration one copy runs with, and where each value comes from
  stack_note      read, set or append to what a stack is for
  stack_up        start a stack in the host daemon
  stack_down      stop a stack's copies, and keep its worktrees
  stack_rm        tear a stack down: worktrees, ports and record
  stack_extend    push back the stop time of a stack
  doctor          what is broken, and the fix for each problem
  cleanup         remove orphaned ports, worktrees and stack records
  env_use         point a scope at a named env
  env_which       the env a service resolves to, rung by rung
  env_set         set a variable on a named env
  investigate     correlated traces + logs in one call        [when observability enabled]
  tunnel          forward local service ports to/from a remote over SSH [when an ssh client is available]

The tool set adapts to the active workspace. The telemetry tools appear only when
the manifest enables observability. The tunnel tools appear only when an ssh
client is available. Call the 'environment' tool first to see what is available.

The tools do not cover every command. Among the commands with no tool: upgrade,
init, ports, dependencies, group add and remove, env list, show and
remove, and every workspace command but topology. Run those in the shell.

TRANSPORT
  stdio (default)   Claude Code and most AI tooling use this
  http              streamable HTTP at /mcp, health check at /healthz
                    --addr sets the listen address (default 127.0.0.1:8765)
                    --tailnet listens on this machine's tailnet IPv4 address
                    it refuses to listen on all interfaces

HTTP AUTHENTICATION
  Every /mcp request needs 'Authorization: Bearer <token>'. /healthz needs none.
  The server refuses to start with no tokens. Give them in one or both of:
  DEVSTACK_MCP_TOKENS_FILE  one '<scope> <name> <sha256-hex>' per line,
                            # comments and blank lines skipped
  DEVSTACK_MCP_TOKENS       scope:name:token,scope:name:token
  A scope is owner or agent. Hash a token for the file with:
    printf %s "$TOKEN" | sha256sum

AGENT LIMITS
  An agent caller acts only on its own agent-* stacks. There is no quota.
  Every stack stops when its stop time passes. devstack never removes it.
  DEVSTACK_STOP_AFTER  time from create or up to the stop (default 24h)
  ~/.devstack/config.json takes stop_after too.
  'devstack stack sweep' stops the stacks. A systemd user timer runs it.`,
	RunE: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("transport", "stdio", "Transport mode: 'http' or 'stdio' (default: stdio)")
	serveCmd.Flags().String("addr", defaultServeAddr, "HTTP listen address, host:port")
	serveCmd.Flags().Bool("tailnet", false, "HTTP: listen on the tailnet IPv4 address, with the port of --addr")
}

const defaultServeAddr = "127.0.0.1:8765"

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

	addr, _ := cmd.Flags().GetString("addr")
	tailnet, _ := cmd.Flags().GetBool("tailnet")
	return serveHTTP(addr, tailnet)
}

func serveStdio() error {
	ws := resolveServeWorkspace(viper.GetString("workspace"))
	mcpServer := buildMCPServer(ws)

	log.Printf("Starting devstack MCP server (workspace: %s, tilt-port: %d)", ws.Name, workspace.HostTiltPort)

	return server.ServeStdio(mcpServer)
}

func buildMCPServer(ws *workspace.Workspace) *server.MCPServer {
	mcpServer := server.NewMCPServer(
		"devstack",
		"1.0.0",
		append([]server.ServerOption{server.WithToolCapabilities(true)}, nvxmcp.GuardOptions(ws)...)...,
	)

	host := viper.GetString("tilt.host")

	defaultService := viper.GetString("default_service")

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
	return mcpServer
}

var tailnetIPv4 = func() (string, error) {
	out, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return "", fmt.Errorf("can not read the tailnet address from 'tailscale ip -4': %w", err)
	}
	ip, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if ip == "" {
		return "", fmt.Errorf("'tailscale ip -4' printed no address")
	}
	return ip, nil
}

func resolveServeAddr(addr string, tailnet bool) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if tailnet {
		if host, err = tailnetIPv4(); err != nil {
			return "", err
		}
	}
	if host == "" || net.ParseIP(host).IsUnspecified() {
		return "", fmt.Errorf("refusing to listen on %q: an empty or unspecified host listens on every interface. Use 127.0.0.1, a specific address, or --tailnet", net.JoinHostPort(host, port))
	}
	return net.JoinHostPort(host, port), nil
}

func newHTTPHandler(mcpServer *server.MCPServer, tokens []auth.Token) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", auth.Middleware(tokens, server.NewStreamableHTTPServer(mcpServer, server.WithEndpointPath("/mcp"))))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func serveHTTP(addr string, tailnet bool) error {
	listenAddr, err := resolveServeAddr(addr, tailnet)
	if err != nil {
		return err
	}
	tokens, err := auth.LoadTokens()
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return fmt.Errorf("the HTTP transport needs at least one token. Set %s or %s", auth.TokensFileEnv, auth.TokensEnv)
	}
	ws := resolveServeWorkspace(viper.GetString("workspace"))
	srv := &http.Server{Addr: listenAddr, Handler: newHTTPHandler(buildMCPServer(ws), tokens)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("Starting devstack MCP server on http://%s/mcp (workspace: %s, tilt-port: %d)", listenAddr, ws.Name, workspace.HostTiltPort)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
