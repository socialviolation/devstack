package hooks

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
)

// ServiceRef is one in-scope service: where its hooks run, the file that
// declares them, and the hooks themselves. For a stack event Path is the
// worktree, not the base checkout, so a service hook runs against the code that
// instance is running. ManifestPath is the file the service came from, which is
// devstack.<name>.yaml where a directory declares more than one service.
type ServiceRef struct {
	Path         string
	ManifestPath string
	Hooks        []config.Hook
}

// manifestFile is the file name that a hook of this service is reported to come
// from. A ServiceRef built without a path falls back to the conventional name.
func (r ServiceRef) manifestFile() string {
	if r.ManifestPath == "" {
		return config.ServiceManifestFileName
	}
	return filepath.Base(r.ManifestPath)
}

// Source is everything hook resolution reads: the workspace-level hooks and the
// services in scope for the event. Workspace hooks always come from the base
// manifest — a stack inherits them the same way it inherits base's environments,
// and a generated stack manifest never declares its own.
type Source struct {
	WorkspaceHooks []config.Hook
	WorkspaceRoot  string
	Services       map[string]ServiceRef
}

// Event is one lifecycle trigger and the context its hooks resolve against.
// Services is the event's service set: the overlay for a stack event, the
// started services for service.start, every service for workspace.up.
type Event struct {
	Name          string
	WorkspaceName string
	Stack         string
	StackRoot     string
	Branch        string
	EnvName       string
	Services      []string
	Book          config.PortBook
}

// StackLabel is the stack an event ran against, spelled the way the CLI and the
// OTEL resource attributes spell it: the short name, or "base".
func (e Event) StackLabel() string {
	if strings.TrimSpace(e.Stack) == "" {
		return "base"
	}
	return e.Stack
}

// Invocation is one hook about to run against one target. Service is empty for a
// hook that fires once per event rather than once per service.
type Invocation struct {
	Hook    config.Hook
	Origin  string
	Service string
	Dir     string
}

// Label identifies an invocation in output and errors.
func (i Invocation) Label() string {
	if i.Service == "" {
		return i.Hook.Name
	}
	return i.Hook.Name + "/" + i.Service
}

// BuildSource assembles hook resolution's input from the base workspace manifest
// (which owns workspace-level hooks) and the resolved workspace whose service
// paths the event acts on — base's own for a base event, the stack's worktree
// workspace for a stack event.
func BuildSource(baseManifest *config.WorkspaceManifest, baseRoot string, rw *config.ResolvedWorkspace) Source {
	src := Source{WorkspaceRoot: baseRoot, Services: map[string]ServiceRef{}}
	if baseManifest != nil {
		src.WorkspaceHooks = baseManifest.Hooks
	}
	if rw == nil {
		return src
	}
	for name, svc := range rw.Services {
		ref := ServiceRef{Path: svc.RepoPath, ManifestPath: svc.ManifestPath}
		if svc.Manifest != nil {
			ref.Hooks = svc.Manifest.Hooks
		}
		src.Services[name] = ref
	}
	return src
}

// Resolve returns the invocations an event fires, in the order they run:
// workspace hooks in manifest order first, then each service's own hooks by
// service name. A workspace hook naming services fans out to one invocation per
// matching service, sorted, so the order is the same on every machine.
//
// A workspace hook that names no services runs once for the whole event; one
// that names services runs only for those also in the event's service set, and
// not at all when none match.
func Resolve(ev Event, src Source) []Invocation {
	inEvent := map[string]bool{}
	for _, s := range ev.Services {
		inEvent[s] = true
	}

	var out []Invocation
	for _, h := range src.WorkspaceHooks {
		if !h.Subscribes(ev.Name) {
			continue
		}
		scoped := h.HookServiceNames()
		if len(scoped) == 0 {
			out = append(out, Invocation{Hook: h, Origin: config.WorkspaceManifestFileName, Dir: src.WorkspaceRoot})
			continue
		}
		for _, svc := range scoped {
			if !inEvent[svc] {
				continue
			}
			out = append(out, Invocation{
				Hook:    h,
				Origin:  config.WorkspaceManifestFileName,
				Service: svc,
				Dir:     src.Services[svc].Path,
			})
		}
	}

	svcNames := append([]string(nil), ev.Services...)
	sort.Strings(svcNames)
	for _, name := range svcNames {
		ref, ok := src.Services[name]
		if !ok {
			continue
		}
		for _, h := range ref.Hooks {
			if !h.Subscribes(ev.Name) {
				continue
			}
			out = append(out, Invocation{
				Hook:    h,
				Origin:  name + "/" + ref.manifestFile(),
				Service: name,
				Dir:     ref.Path,
			})
		}
	}
	return out
}

// resolveDir is where a hook's command runs: its own workDir if it sets one
// (relative paths resolve against the invocation's directory), else that
// directory — the workspace root for an event-scoped hook, the service's repo or
// worktree for a service-scoped one.
func (i Invocation) resolveDir() string {
	wd := strings.TrimSpace(i.Hook.WorkDir)
	if wd == "" {
		return i.Dir
	}
	if strings.HasPrefix(wd, "/") {
		return wd
	}
	return i.Dir + "/" + wd
}

// expand resolves ${service.field} references in s against the event's port
// book, with the invocation's service as ${self}.
func (i Invocation) expand(s string, book config.PortBook) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	if i.Service == "" && strings.Contains(s, "${self.") {
		return "", fmt.Errorf("hook %q uses ${self...}, but it is not scoped to a service. Add 'services:' so that devstack knows which service ${self} means", i.Hook.Name)
	}
	return config.ResolveRefs(s, i.Service, book)
}
