package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tunnel"
	"github.com/socialviolation/devstack/internal/workspace"
)

// newTunnelToolServer registers the tunnel tool against a daemon view holding
// one base service, under a temp HOME so PID files land in the sandbox.
func newTunnelToolServer(t *testing.T, view string) (*server.MCPServer, *workspace.Workspace) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(view))
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	ws := &workspace.Workspace{Name: "ws", TiltPort: port}
	s := server.NewMCPServer("test", "0.0.0")
	registerTunnelTool(s, tilt.NewClient(u.Hostname(), port), ws)
	return s, ws
}

// trackForward fakes a live forward: a real, killable child process plus the
// PID file tunnel bookkeeping reads. It must not be this process — stopping a
// tunnel signals the tracked PID.
func trackForward(t *testing.T, wsName string, port int) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	dir := tunnel.Dir(wsName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.pid", port)), []byte(pid), 0644); err != nil {
		t.Fatal(err)
	}
}

// Ports chosen to be unlikely to match a real forward on the developer's
// machine: tearing a tunnel down also sweeps untracked ssh processes bound to
// the same port.
const (
	apiPort   = 59001
	otelPort  = 59002
	stackPort = 59003
)

const oneBaseService = `{"uiResources":[{"metadata":{"name":"ws:api"},"status":{"runtimeStatus":"ok","endpointLinks":[{"url":"http://localhost:59001"}]}}]}`

// A forward outlives the thing that created it. Stopping only what discovery
// currently covers leaves the observability UI and any stack forward running
// while reporting success — the agent then tells its user the tunnels are down.
func TestMCPTunnelStopKillsForwardsDiscoveryNoLongerCovers(t *testing.T) {
	s, ws := newTunnelToolServer(t, oneBaseService)
	trackForward(t, ws.Name, apiPort)   // discovered
	trackForward(t, ws.Name, otelPort)  // the otel UI, never a daemon resource
	trackForward(t, ws.Name, stackPort) // a stack's port, not asked for here

	out := callTool(t, s, "tunnel", map[string]string{"action": "stop"})

	if left := tunnel.TrackedPorts(ws.Name); len(left) != 0 {
		t.Errorf("still forwarding %v after stop; reported: %s", left, out)
	}
	for _, port := range []string{":59001", ":59002", ":59003"} {
		if !strings.Contains(out, port) {
			t.Errorf("stop did not name %s: %s", port, out)
		}
	}
}

func TestMCPTunnelStopHonoursTheServiceFilter(t *testing.T) {
	s, ws := newTunnelToolServer(t, oneBaseService)
	trackForward(t, ws.Name, apiPort)
	trackForward(t, ws.Name, otelPort)

	callTool(t, s, "tunnel", map[string]string{"action": "stop", "service": "api"})

	left := tunnel.TrackedPorts(ws.Name)
	if len(left) != 1 || left[0] != otelPort {
		t.Errorf("tunnels left = %v, want only :%d", left, otelPort)
	}
}

func TestMCPTunnelStopWithNothingRunning(t *testing.T) {
	s, _ := newTunnelToolServer(t, oneBaseService)
	if out := callTool(t, s, "tunnel", map[string]string{"action": "stop"}); !strings.Contains(out, "No tunnels running") {
		t.Errorf("expected a no-tunnels message, got: %s", out)
	}
}

// A live forward for something discovery has lost must still be visible, or the
// only way to find it is to notice the port is busy.
func TestMCPTunnelStatusShowsUntrackedByDiscovery(t *testing.T) {
	s, ws := newTunnelToolServer(t, oneBaseService)
	trackForward(t, ws.Name, otelPort)

	out := callTool(t, s, "tunnel", map[string]string{"action": "status"})
	if !strings.Contains(out, "59002") {
		t.Errorf("status hid a live forward: %s", out)
	}
	if !strings.Contains(out, "no longer discovered") {
		t.Errorf("status did not say the forward is undiscovered: %s", out)
	}
}

// A mapped forward reported as one port hands back the stack's own port — the
// one address the far end must not be told to use.
func TestPortLabelNamesBothEndsWhenMapped(t *testing.T) {
	plain := tunnel.Service{Name: "api", Port: 63290}
	if got := portLabel(plain); got != ":63290" {
		t.Errorf("portLabel(plain) = %q, want \":63290\"", got)
	}
	mapped := tunnel.Service{Name: "api:agent", Port: 20005, RemotePort: 63290}
	if got := portLabel(mapped); got != "far end :63290 → here :20005" {
		t.Errorf("portLabel(mapped) = %q", got)
	}
}

// The CLI flag is --service and the names it takes are exact. A tool parameter
// spelled differently is one an agent fills from the CLI's documentation and
// watches be ignored — the call then forwards or tears down everything.
func TestMCPTunnelNamesTheServiceParamAsTheCLIDoes(t *testing.T) {
	s, _ := newTunnelToolServer(t, oneBaseService)
	listing := clarityToolListing(t, s)

	if !strings.Contains(listing, `"service"`) {
		t.Errorf("tunnel tool does not take a 'service' parameter: %s", listing)
	}
	if strings.Contains(listing, `"services"`) {
		t.Errorf("tunnel tool still takes the old 'services' parameter: %s", listing)
	}
}

// Reclaim kills whatever holds the far host's ports, and cannot tell a stale
// forward of yours from a live one another stack owns. Without a check action
// the only way to look first is to leave MCP for a shell.
func TestMCPTunnelOffersCheckBeforeReclaim(t *testing.T) {
	s, _ := newTunnelToolServer(t, oneBaseService)
	if !strings.Contains(clarityToolListing(t, s), "check") {
		t.Error("tunnel tool does not advertise the check action")
	}

	out := callTool(t, s, "tunnel", map[string]string{"action": "sniff"})
	if !strings.Contains(out, "check") {
		t.Errorf("unknown-action message does not name check: %s", out)
	}
}
