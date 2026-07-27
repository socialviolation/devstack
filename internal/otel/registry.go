package otel

import (
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/socialviolation/devstack/internal/observability"
	"github.com/socialviolation/devstack/internal/workspace"
)

// DefaultPlugin is the backend a workspace gets when it does not name one.
const DefaultPlugin = "openobserve"

var plugins = map[string]Plugin{}

// warnedPlugins keeps one command run from repeating the same warning for every
// workspace it touches.
var warnedPlugins sync.Map

// WarnWriter receives plugin-resolution warnings; tests redirect it.
var WarnWriter io.Writer = os.Stderr

func warnUnknownPlugin(name string) {
	if _, loaded := warnedPlugins.LoadOrStore(name, true); loaded {
		return
	}
	fmt.Fprintf(WarnWriter,
		"warning: observability backend %q is not known to this devstack build — falling back to %q.\n",
		name, DefaultPlugin)
	fmt.Fprintf(WarnWriter,
		"         if you expected %q, your installed devstack is out of date: reinstall with 'go install ./...'\n",
		name)
}

// Register adds a plugin to the global registry.
// Typically called from plugin init() functions.
func Register(p Plugin) {
	plugins[p.Name()] = p
}

// Get returns the plugin with the given name.
// Falls back to DefaultPlugin if name is unknown or empty.
func Get(name string) Plugin {
	if name != "" {
		if p, ok := plugins[name]; ok {
			return p
		}
		// A configured-but-unknown backend means this binary predates it (or the
		// name is a typo). Falling back silently is how a workspace ends up
		// running infrastructure nobody asked for, so say so.
		warnUnknownPlugin(name)
	}
	if p, ok := plugins[DefaultPlugin]; ok {
		return p
	}
	// Last resort: return first registered plugin
	for _, p := range plugins {
		return p
	}
	return nil
}

// For returns the plugin a workspace's telemetry is handled by: the one it
// names, otherwise the default. Every caller that needs to reach a workspace's
// observability goes through here rather than naming a backend itself.
func For(ws *workspace.Workspace) Plugin {
	if ws == nil {
		return Get("")
	}
	return Get(ws.OtelPlugin)
}

// BackendFor returns a ready-to-query client for whatever backend the workspace
// is configured with — resolved, addressed and authenticated for the caller.
// This is the single entry point the CLI and MCP tools use to run a query.
func BackendFor(ws *workspace.Workspace) (observability.Backend, error) {
	if ws == nil {
		return nil, fmt.Errorf("no workspace resolved — run from inside a workspace directory")
	}
	p := For(ws)
	if p == nil {
		return nil, fmt.Errorf("no OTEL plugin registered")
	}
	backend, err := p.Backend(ws)
	if err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, fmt.Errorf("plugin %q exposes no queryable backend", p.Name())
	}
	// One backend holds every workspace on the machine, so the scope is applied
	// here rather than trusted to each caller.
	return observability.ScopedTo(backend, ws.Name), nil
}

// QueryEndpointFor returns the UI/query URL of whatever backend the workspace
// is configured with, or "" when it has no local one.
func QueryEndpointFor(ws *workspace.Workspace) string {
	p := For(ws)
	if p == nil {
		return ""
	}
	return p.QueryEndpoint(ws)
}

// All returns all registered plugins sorted by name.
func All() []Plugin {
	result := make([]Plugin, 0, len(plugins))
	for _, p := range plugins {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result
}
