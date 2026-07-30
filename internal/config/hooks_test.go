package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadWorkspaceManifestParsesHooks(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
hooks:
  - name: auth0-callbacks
    on: [stack.up, stack.destroy]
    services: [frontend]
    run: ./scripts/auth0.sh ${self.url}
    workDir: tools
    env:
      CALLBACK: ${self.url}/callback
    timeout: 90s
    onError: continue
`)

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest(): %v", err)
	}
	if len(m.Hooks) != 1 {
		t.Fatalf("hooks = %d, want 1", len(m.Hooks))
	}
	h := m.Hooks[0]
	if h.Name != "auth0-callbacks" {
		t.Errorf("name = %q", h.Name)
	}
	if !h.Subscribes(EventStackUp) || !h.Subscribes(EventStackDestroy) {
		t.Errorf("on = %v, want both stack.up and stack.destroy", h.On)
	}
	if h.Subscribes(EventStackDown) {
		t.Errorf("hook should not subscribe to stack.down")
	}
	if got := h.HookServiceNames(); len(got) != 1 || got[0] != "frontend" {
		t.Errorf("services = %v", got)
	}
	if h.Env["CALLBACK"] != "${self.url}/callback" {
		t.Errorf("env not parsed verbatim: %q", h.Env["CALLBACK"])
	}
	if got := h.ResolvedTimeout(); got != 90*time.Second {
		t.Errorf("timeout = %s, want 90s", got)
	}
	if got := h.ResolvedOnError(EventStackUp); got != OnErrorContinue {
		t.Errorf("onError = %q, want %q", got, OnErrorContinue)
	}
}

func TestLoadServiceManifestParsesHooks(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ServiceManifestFileName), `version: 1
service:
  name: frontend
runtime:
  run:
    command: npm start
hooks:
  - name: notify
    on: [service.start]
    run: echo started
`)

	m, err := LoadServiceManifest(dir)
	if err != nil {
		t.Fatalf("LoadServiceManifest(): %v", err)
	}
	if len(m.Hooks) != 1 || m.Hooks[0].Name != "notify" {
		t.Fatalf("hooks = %#v", m.Hooks)
	}
}

func TestHookDefaultsFillInWhenUnset(t *testing.T) {
	h := Hook{Name: "x", On: []string{EventStackUp}, Run: "true"}
	if got := h.ResolvedTimeout(); got != DefaultHookTimeout {
		t.Errorf("timeout = %s, want %s", got, DefaultHookTimeout)
	}
	if got := h.ResolvedOnError(EventStackUp); got != OnErrorAbort {
		t.Errorf("setup event policy = %q, want abort", got)
	}
	if got := h.ResolvedOnError(EventStackDestroy); got != OnErrorContinue {
		t.Errorf("teardown event policy = %q, want continue", got)
	}
}

// A teardown hook must never be able to block removal by default: a stack you
// cannot delete leaks a worktree, a branch and a port every time.
func TestTeardownEventsDefaultToContinue(t *testing.T) {
	for _, event := range []string{EventStackDestroy, EventStackDown, EventServiceStop, EventWorkspaceDown} {
		if got := DefaultOnError(event); got != OnErrorContinue {
			t.Errorf("DefaultOnError(%s) = %q, want continue", event, got)
		}
		if !IsTeardownEvent(event) {
			t.Errorf("IsTeardownEvent(%s) = false", event)
		}
	}
	for _, event := range []string{EventStackCreate, EventStackUp, EventServiceStart, EventWorkspaceUp} {
		if got := DefaultOnError(event); got != OnErrorAbort {
			t.Errorf("DefaultOnError(%s) = %q, want abort", event, got)
		}
	}
}

func TestValidateHooksRejectsBadDefinitions(t *testing.T) {
	tests := []struct {
		name          string
		hook          Hook
		allowServices bool
		wantErr       string
	}{
		{
			name:          "missing name",
			hook:          Hook{On: []string{EventStackUp}, Run: "true"},
			allowServices: true,
			wantErr:       "name is required",
		},
		{
			name:          "no events",
			hook:          Hook{Name: "x", Run: "true"},
			allowServices: true,
			wantErr:       "at least one event",
		},
		{
			name:          "unknown event",
			hook:          Hook{Name: "x", On: []string{"stack.exploded"}, Run: "true"},
			allowServices: true,
			wantErr:       `unknown event "stack.exploded"`,
		},
		{
			name:          "missing run",
			hook:          Hook{Name: "x", On: []string{EventStackUp}},
			allowServices: true,
			wantErr:       "needs a 'run' command",
		},
		{
			name:          "services in a service manifest",
			hook:          Hook{Name: "x", On: []string{EventStackUp}, Run: "true", Services: []string{"api"}},
			allowServices: false,
			wantErr:       "only applies to workspace hooks",
		},
		{
			name:          "unparseable timeout",
			hook:          Hook{Name: "x", On: []string{EventStackUp}, Run: "true", Timeout: "soon"},
			allowServices: true,
			wantErr:       "unparseable timeout",
		},
		{
			name:          "negative timeout",
			hook:          Hook{Name: "x", On: []string{EventStackUp}, Run: "true", Timeout: "-5s"},
			allowServices: true,
			wantErr:       "non-positive timeout",
		},
		{
			name:          "bad error policy",
			hook:          Hook{Name: "x", On: []string{EventStackUp}, Run: "true", OnError: "explode"},
			allowServices: true,
			wantErr:       `onError "explode"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHooks([]Hook{tc.hook}, "test.yaml", tc.allowServices)
			if err == nil {
				t.Fatalf("validateHooks() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateHooks() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateHooksRejectsDuplicateNames(t *testing.T) {
	hooks := []Hook{
		{Name: "dup", On: []string{EventStackUp}, Run: "true"},
		{Name: "dup", On: []string{EventStackDown}, Run: "true"},
	}
	err := validateHooks(hooks, "test.yaml", true)
	if err == nil || !strings.Contains(err.Error(), "duplicate hook name") {
		t.Fatalf("validateHooks() = %v, want duplicate name error", err)
	}
}

// A hook whose event name is a typo must fail at load, not silently never fire.
func TestLoadWorkspaceManifestRejectsUnknownEvent(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos: [./api]
hooks:
  - name: typo
    on: [stack.upp]
    run: 'true'
`)
	_, err := LoadWorkspaceManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("LoadWorkspaceManifest() = %v, want unknown event error", err)
	}
}
