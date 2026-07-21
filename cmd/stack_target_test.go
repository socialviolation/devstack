package cmd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func portFromURL(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port from %q: %v", raw, err)
	}
	return p
}

// With base's daemon up and the stack active, resolveStackTarget addresses base's
// port with the stack's namespace — never a dead per-stack daemon port. Mutating
// it to return rec.DaemonPort (0) instead of base.TiltPort fails the port
// assertion; dropping the namespace fails the namespace/resourceName assertions.
func TestResolveStackTargetUsesBasePortAndNamespace(t *testing.T) {
	rec, _ := buildStackScenario(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"uiResources":[]}`))
	}))
	defer srv.Close()

	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	base.TiltPort = portFromURL(t, srv.URL)

	if err := stack.SetActive(base.Name, rec.Name, true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	port, namespace, root, label, err := resolveStackTarget(base, "feat")
	if err != nil {
		t.Fatalf("resolveStackTarget: %v", err)
	}
	if port != base.TiltPort {
		t.Errorf("port = %d, want base port %d (must not use a per-stack daemon port)", port, base.TiltPort)
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
	if rn := resourceName("backend", namespace); rn != "backend:feat" {
		t.Errorf("resourceName = %q, want the namespaced resource %q", rn, "backend:feat")
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
	if port != base.TiltPort || namespace != "" || root != base.Path || label != "" {
		t.Errorf("base target = (%d, %q, %q, %q), want (%d, \"\", %q, \"\")",
			port, namespace, root, label, base.TiltPort, base.Path)
	}
}
