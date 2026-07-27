package otel

import (
	"bytes"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/observability"
	"github.com/socialviolation/devstack/internal/workspace"
)

// stubPlugin stands in for a real backend. The plugin packages import this one,
// so tests here register their own rather than importing them back.
type stubPlugin struct{ name string }

func (p *stubPlugin) Name() string { return p.name }
func (p *stubPlugin) Contribute(*workspace.Workspace) (Contribution, error) {
	return Contribution{}, nil
}
func (p *stubPlugin) StartCompanion(*workspace.Workspace) error  { return nil }
func (p *stubPlugin) StopCompanion(*workspace.Workspace) error   { return nil }
func (p *stubPlugin) CompanionRunning(*workspace.Workspace) bool { return true }
func (p *stubPlugin) CompanionStale(*workspace.Workspace) bool   { return false }
func (p *stubPlugin) QueryEndpoint(*workspace.Workspace) string  { return "" }
func (p *stubPlugin) Backend(*workspace.Workspace) (observability.Backend, error) {
	return nil, nil
}
func (p *stubPlugin) Validate(*workspace.Workspace) error { return nil }
func (p *stubPlugin) ConfigSchema() []ConfigField         { return nil }

func registerStubs(t *testing.T) {
	t.Helper()
	for _, name := range []string{DefaultPlugin, "forwarding"} {
		if _, ok := plugins[name]; ok {
			continue
		}
		Register(&stubPlugin{name: name})
		t.Cleanup(func() { delete(plugins, name) })
	}
}

// An unknown backend name means the binary predates it. Falling back silently is
// how a workspace ends up running a backend nobody asked for, so the fallback
// must announce itself.
func TestGetWarnsOnUnknownPlugin(t *testing.T) {
	registerStubs(t)
	var buf bytes.Buffer
	WarnWriter = &buf
	warnedPlugins.Delete("from-the-future")
	t.Cleanup(func() { warnedPlugins.Delete("from-the-future") })

	got := Get("from-the-future")
	if got == nil || got.Name() != DefaultPlugin {
		t.Fatalf("fell back to %v, want %s", got, DefaultPlugin)
	}
	out := buf.String()
	if !strings.Contains(out, "from-the-future") || !strings.Contains(out, DefaultPlugin) {
		t.Errorf("warning did not name the plugin and the fallback: %q", out)
	}
	if !strings.Contains(out, "go install") {
		t.Errorf("warning should say how to fix a stale install: %q", out)
	}

	// Repeated resolution must not repeat the warning.
	buf.Reset()
	Get("from-the-future")
	if buf.Len() != 0 {
		t.Errorf("warning repeated: %q", buf.String())
	}
}

func TestGetKnownPluginIsSilent(t *testing.T) {
	registerStubs(t)
	var buf bytes.Buffer
	WarnWriter = &buf

	if p := Get(DefaultPlugin); p == nil || p.Name() != DefaultPlugin {
		t.Fatalf("Get(%q) = %v", DefaultPlugin, p)
	}
	if buf.Len() != 0 {
		t.Errorf("resolving a known plugin warned: %q", buf.String())
	}
}

func TestForUsesWorkspacePlugin(t *testing.T) {
	registerStubs(t)
	var buf bytes.Buffer
	WarnWriter = &buf

	if p := For(&workspace.Workspace{Name: "ws", OtelPlugin: "forwarding"}); p == nil || p.Name() != "forwarding" {
		t.Errorf("For() did not honour the workspace's plugin: %v", p)
	}
	if p := For(nil); p == nil || p.Name() != DefaultPlugin {
		t.Errorf("For(nil) = %v, want the default", p)
	}
}
