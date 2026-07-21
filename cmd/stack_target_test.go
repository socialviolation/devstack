package cmd

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// serveHostDaemon starts an httptest server bound to the fixed host daemon port so
// the reachability checks in resolveStackTarget (which dial HostTiltPort) succeed.
// If the port is unavailable the test is skipped rather than reported as a failure.
func serveHostDaemon(t *testing.T) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", workspace.HostTiltPort))
	if err != nil {
		t.Skipf("host port %d unavailable: %v", workspace.HostTiltPort, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"uiResources":[]}`))
	}))
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	return srv
}

// With the host daemon up and the stack active, resolveStackTarget addresses the
// host port with the stack's namespace — never a dead per-stack daemon port.
// Mutating it to return rec.DaemonPort (0) instead of HostTiltPort fails the port
// assertion; dropping the namespace fails the namespace/resourceName assertions.
func TestResolveStackTargetUsesBasePortAndNamespace(t *testing.T) {
	rec, _ := buildStackScenario(t)

	srv := serveHostDaemon(t)
	defer srv.Close()

	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}

	if err := stack.SetActive(base.Name, rec.Name, true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	port, namespace, root, label, err := resolveStackTarget(base, "feat")
	if err != nil {
		t.Fatalf("resolveStackTarget: %v", err)
	}
	if port != workspace.HostTiltPort {
		t.Errorf("port = %d, want host port %d (must not use a per-stack daemon port)", port, workspace.HostTiltPort)
	}
	if namespace != "feat" {
		t.Errorf("namespace = %q, want the stack name %q", namespace, "feat")
	}
	if root != rec.Root {
		t.Errorf("root = %q, want stack root %q", root, rec.Root)
	}
	if !strings.Contains(label, "feat") {
		t.Errorf("label = %q, want it to name the stack", label)
	}
	if rn := resourceName(base.Name, "backend", namespace); rn != "navexa:backend:feat" {
		t.Errorf("resourceName = %q, want the host-namespaced resource %q", rn, "navexa:backend:feat")
	}
}

// A stack that is not up (base daemon down, or the stack inactive) gives the
// "not up" guidance and never returns a target to dial.
func TestResolveStackTargetInactiveStackNotUp(t *testing.T) {
	rec, _ := buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	_ = rec

	_, _, _, _, err = resolveStackTarget(base, "feat")
	if err == nil {
		t.Fatal("expected an error for a stack that is not up")
	}
	if !strings.Contains(err.Error(), "not up") || !strings.Contains(err.Error(), "devstack stack up feat") {
		t.Errorf("error should give the 'not up' guidance, got: %v", err)
	}
}

// The base itself (empty stack) resolves to base's port with no namespace,
// byte-identical to today.
func TestResolveStackTargetBaseNoNamespace(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}

	port, namespace, root, label, err := resolveStackTarget(base, "")
	if err != nil {
		t.Fatalf("resolveStackTarget: %v", err)
	}
	if port != workspace.HostTiltPort || namespace != "" || root != base.Path || label != "" {
		t.Errorf("base target = (%d, %q, %q, %q), want (%d, \"\", %q, \"\")",
			port, namespace, root, label, workspace.HostTiltPort, base.Path)
	}
}

// resourceName must match tiltgen's hostName scheme exactly: <ws>:<svc> for a base
// service and <ws>:<svc>:<stack> for a stack overlay. Drift here means the service
// control commands would address resources the host daemon does not have.
func TestResourceNameHostScheme(t *testing.T) {
	if got := resourceName("navexa", "api", ""); got != "navexa:api" {
		t.Errorf("resourceName(navexa, api, \"\") = %q, want %q", got, "navexa:api")
	}
	if got := resourceName("navexa", "api", "perf"); got != "navexa:api:perf" {
		t.Errorf("resourceName(navexa, api, perf) = %q, want %q", got, "navexa:api:perf")
	}
}
