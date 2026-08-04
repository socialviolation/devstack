package config

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Lifecycle events a hook can subscribe to. Each name is paired with the exact
// point devstack fires it, because a hook that provisions an external resource
// needs to know what is already true when it runs.
const (
	// EventStackCreate fires after a stack's worktrees exist, its ports are
	// allocated and its record is written — so a hook sees final ports.
	EventStackCreate = "stack.create"
	// EventStackDestroy fires before any worktree, port or record is removed,
	// so a teardown hook can still read what the stack was allocated.
	EventStackDestroy = "stack.destroy"
	// EventStackUp fires after the stack's services have been triggered in the
	// host daemon.
	EventStackUp = "stack.up"
	// EventStackDown fires before the host Tiltfile is regenerated to drop the
	// stack's resources.
	EventStackDown = "stack.down"
	// EventServiceStart fires after a service (and its dependencies) have been
	// triggered.
	EventServiceStart = "service.start"
	// EventServiceStop fires after a service has been disabled.
	EventServiceStop = "service.stop"
	// EventWorkspaceUp fires after the host daemon is up and the workspace's
	// services are folded into it.
	EventWorkspaceUp = "workspace.up"
	// EventWorkspaceDown fires before the workspace's services are torn down.
	EventWorkspaceDown = "workspace.down"
)

// HookEvents lists every event a hook may subscribe to, in lifecycle order.
func HookEvents() []string {
	return []string{
		EventWorkspaceUp, EventWorkspaceDown,
		EventStackCreate, EventStackUp, EventStackDown, EventStackDestroy,
		EventServiceStart, EventServiceStop,
	}
}

// Error policies for a failing hook.
const (
	OnErrorAbort    = "abort"
	OnErrorContinue = "continue"
)

// DefaultHookTimeout bounds a hook that never returns. A hook talking to a
// remote API is the common case, so the default is generous rather than tight.
const DefaultHookTimeout = 60 * time.Second

// Hook is one lifecycle action: a shell command devstack runs when one of the
// events in On fires. Run, WorkDir and the values of Env are resolved for
// ${service.field} references before execution, so a hook can name the port or
// URL a stack was allocated without knowing it in advance.
type Hook struct {
	Name string   `yaml:"name"`
	On   []string `yaml:"on"`
	// Services scopes a workspace-manifest hook to named services: it then runs
	// once per matching service in the event's service set, with that service as
	// ${self}. Empty means the hook runs once per event, whatever the services.
	// Not permitted in a service manifest, where the owning service is implied.
	Services []string          `yaml:"services,omitempty"`
	Run      string            `yaml:"run"`
	WorkDir  string            `yaml:"workDir,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
	// Timeout is a Go duration string ("30s", "2m"). Empty means DefaultHookTimeout.
	Timeout string `yaml:"timeout,omitempty"`
	// OnError is "abort" or "continue". Empty defers to the event: setup events
	// abort, teardown events continue. See DefaultOnError.
	OnError string `yaml:"onError,omitempty"`
}

// ResolvedTimeout is the hook's timeout, or the default when it names none.
// Validation has already rejected an unparseable value.
func (h Hook) ResolvedTimeout() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(h.Timeout))
	if err != nil || d <= 0 {
		return DefaultHookTimeout
	}
	return d
}

// ResolvedOnError is the hook's error policy for one event: its own OnError if
// set, otherwise the event's default.
func (h Hook) ResolvedOnError(event string) string {
	if p := strings.TrimSpace(h.OnError); p != "" {
		return p
	}
	return DefaultOnError(event)
}

// DefaultOnError is the error policy an event applies to a hook that names
// none. Setup events abort: a stack whose provisioning failed is worse than no
// stack. Teardown events continue, so the remaining hooks still run.
//
// On a teardown event the policy governs the hook chain only. It never governs
// the lifecycle action. A teardown always proceeds, even when a hook sets
// onError "abort" and stops the hooks behind it — you must never be unable to
// remove a stack because a hook is broken, or every failure leaks a worktree, a
// port and a record.
func DefaultOnError(event string) string {
	switch event {
	case EventStackDestroy, EventStackDown, EventServiceStop, EventWorkspaceDown:
		return OnErrorContinue
	default:
		return OnErrorAbort
	}
}

// IsTeardownEvent reports whether an event runs while something is being taken
// away rather than brought up.
func IsTeardownEvent(event string) bool {
	return DefaultOnError(event) == OnErrorContinue
}

func isKnownEvent(name string) bool {
	for _, e := range HookEvents() {
		if e == name {
			return true
		}
	}
	return false
}

// validateHooks checks a manifest's hooks. scope names the manifest for error
// messages; allowServices is false for service manifests, where a hook's scope
// is the owning service and naming others would silently do nothing.
func validateHooks(hooks []Hook, scope string, allowServices bool) error {
	seen := map[string]bool{}
	for i, h := range hooks {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			return fmt.Errorf("%s: hooks[%d].name is required", scope, i)
		}
		if seen[name] {
			return fmt.Errorf("%s: duplicate hook name %q", scope, name)
		}
		seen[name] = true

		if len(h.On) == 0 {
			return fmt.Errorf("%s: hook %q needs at least one event in 'on' (known events: %s)", scope, name, strings.Join(HookEvents(), ", "))
		}
		for _, e := range h.On {
			if !isKnownEvent(strings.TrimSpace(e)) {
				return fmt.Errorf("%s: hook %q subscribes to unknown event %q (known events: %s)", scope, name, e, strings.Join(HookEvents(), ", "))
			}
		}
		if strings.TrimSpace(h.Run) == "" {
			return fmt.Errorf("%s: hook %q needs a 'run' command", scope, name)
		}
		if !allowServices && len(h.Services) > 0 {
			return fmt.Errorf("%s: hook %q sets 'services', but 'services' applies only to workspace hooks. The hooks of a service manifest are already scoped to that service", scope, name)
		}
		if t := strings.TrimSpace(h.Timeout); t != "" {
			d, err := time.ParseDuration(t)
			if err != nil {
				return fmt.Errorf("%s: hook %q has the timeout %q, and devstack can not parse it: %w", scope, name, h.Timeout, err)
			}
			if d <= 0 {
				return fmt.Errorf("%s: hook %q has the timeout %q, and a timeout must be more than zero", scope, name, h.Timeout)
			}
		}
		if p := strings.TrimSpace(h.OnError); p != "" && p != OnErrorAbort && p != OnErrorContinue {
			return fmt.Errorf("%s: hook %q has onError %q, and onError must be %q or %q", scope, name, h.OnError, OnErrorAbort, OnErrorContinue)
		}
	}
	return nil
}

// HookServiceNames returns the services a workspace hook is scoped to, sorted,
// so a hook fires in the same order on every machine.
func (h Hook) HookServiceNames() []string {
	out := make([]string, 0, len(h.Services))
	for _, s := range h.Services {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Subscribes reports whether the hook listens for the named event.
func (h Hook) Subscribes(event string) bool {
	for _, e := range h.On {
		if strings.TrimSpace(e) == event {
			return true
		}
	}
	return false
}
