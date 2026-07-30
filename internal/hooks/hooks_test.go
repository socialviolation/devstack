package hooks

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func labels(invocations []Invocation) []string {
	out := make([]string, 0, len(invocations))
	for _, i := range invocations {
		out = append(out, i.Label())
	}
	return out
}

func testSource() Source {
	return Source{
		WorkspaceRoot: "/ws",
		WorkspaceHooks: []config.Hook{
			{Name: "ws-wide", On: []string{config.EventStackUp}, Run: "true"},
			{Name: "per-svc", On: []string{config.EventStackUp}, Run: "true", Services: []string{"frontend", "api"}},
			{Name: "other-event", On: []string{config.EventStackDown}, Run: "true"},
		},
		Services: map[string]ServiceRef{
			"api": {Path: "/ws/api", Hooks: []config.Hook{
				{Name: "api-own", On: []string{config.EventStackUp}, Run: "true"},
			}},
			"frontend": {Path: "/ws/frontend", Hooks: []config.Hook{
				{Name: "fe-own", On: []string{config.EventStackUp}, Run: "true"},
			}},
			"worker": {Path: "/ws/worker"},
		},
	}
}

// Workspace hooks run before service hooks, a fan-out hook runs once per service
// in sorted order, and service hooks follow by service name — so the order is
// identical on every machine.
func TestResolveOrdersWorkspaceHooksThenServiceHooks(t *testing.T) {
	ev := Event{Name: config.EventStackUp, Services: []string{"frontend", "api"}}
	got := labels(Resolve(ev, testSource()))
	want := []string{"ws-wide", "per-svc/api", "per-svc/frontend", "api-own/api", "fe-own/frontend"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestResolveSkipsHooksForOtherEvents(t *testing.T) {
	ev := Event{Name: config.EventStackDown, Services: []string{"api"}}
	got := labels(Resolve(ev, testSource()))
	if len(got) != 1 || got[0] != "other-event" {
		t.Fatalf("resolved = %v, want [other-event]", got)
	}
}

// A hook scoped to services none of which are in the event's set must not fire.
func TestResolveDropsScopedHookWhenNoServiceMatches(t *testing.T) {
	ev := Event{Name: config.EventStackUp, Services: []string{"worker"}}
	got := labels(Resolve(ev, testSource()))
	if len(got) != 1 || got[0] != "ws-wide" {
		t.Fatalf("resolved = %v, want [ws-wide] — per-svc names no service in the event", got)
	}
}

// A service-scoped invocation runs in that service's directory, which for a
// stack event is the worktree; an event-scoped one runs at the workspace root.
func TestResolveSetsWorkingDirectory(t *testing.T) {
	ev := Event{Name: config.EventStackUp, Services: []string{"api"}}
	invocations := Resolve(ev, testSource())
	dirs := map[string]string{}
	for _, i := range invocations {
		dirs[i.Label()] = i.resolveDir()
	}
	if got := dirs["ws-wide"]; got != "/ws" {
		t.Errorf("ws-wide dir = %q, want /ws", got)
	}
	if got := dirs["per-svc/api"]; got != "/ws/api" {
		t.Errorf("per-svc/api dir = %q, want /ws/api", got)
	}
	if got := dirs["api-own/api"]; got != "/ws/api" {
		t.Errorf("api-own dir = %q, want /ws/api", got)
	}
}

func TestResolveDirAppliesHookWorkDir(t *testing.T) {
	rel := Invocation{Hook: config.Hook{WorkDir: "tools"}, Dir: "/ws"}
	if got := rel.resolveDir(); got != "/ws/tools" {
		t.Errorf("relative workDir = %q, want /ws/tools", got)
	}
	abs := Invocation{Hook: config.Hook{WorkDir: "/opt/scripts"}, Dir: "/ws"}
	if got := abs.resolveDir(); got != "/opt/scripts" {
		t.Errorf("absolute workDir = %q, want /opt/scripts", got)
	}
}

func TestBuildSourceTakesWorkspaceHooksFromBaseAndPathsFromResolved(t *testing.T) {
	base := &config.WorkspaceManifest{
		Hooks: []config.Hook{{Name: "inherited", On: []string{config.EventStackUp}, Run: "true"}},
	}
	rw := &config.ResolvedWorkspace{Services: map[string]config.ResolvedService{
		"api": {
			Name:     "api",
			RepoPath: "/stacks/feat/api",
			Manifest: &config.ServiceManifest{Hooks: []config.Hook{{Name: "api-own", On: []string{config.EventStackUp}, Run: "true"}}},
		},
	}}

	src := BuildSource(base, "/ws", rw)
	if len(src.WorkspaceHooks) != 1 || src.WorkspaceHooks[0].Name != "inherited" {
		t.Fatalf("workspace hooks = %#v", src.WorkspaceHooks)
	}
	if src.WorkspaceRoot != "/ws" {
		t.Errorf("workspace root = %q, want /ws", src.WorkspaceRoot)
	}
	if got := src.Services["api"].Path; got != "/stacks/feat/api" {
		t.Errorf("api path = %q, want the worktree path", got)
	}
	if len(src.Services["api"].Hooks) != 1 {
		t.Errorf("api hooks = %#v", src.Services["api"].Hooks)
	}
}

func TestStackLabelSpellsBaseExplicitly(t *testing.T) {
	if got := (Event{}).StackLabel(); got != "base" {
		t.Errorf("empty stack label = %q, want base", got)
	}
	if got := (Event{Stack: "feat"}).StackLabel(); got != "feat" {
		t.Errorf("stack label = %q, want feat", got)
	}
}

func TestExpandRejectsSelfWithoutAService(t *testing.T) {
	inv := Invocation{Hook: config.Hook{Name: "x", Run: "echo ${self.url}"}}
	_, err := inv.expand(inv.Hook.Run, config.PortBook{})
	if err == nil || !strings.Contains(err.Error(), "not scoped to a service") {
		t.Fatalf("expand() = %v, want a clear ${self} scoping error", err)
	}
}

func TestExpandResolvesSelfAgainstTheEventsPortBook(t *testing.T) {
	book := config.PortBook{"frontend": {"http": 20006}}
	inv := Invocation{Hook: config.Hook{Name: "x"}, Service: "frontend"}
	got, err := inv.expand("${self.url}/callback", book)
	if err != nil {
		t.Fatalf("expand(): %v", err)
	}
	if got != "http://localhost:20006/callback" {
		t.Fatalf("expand() = %q", got)
	}
}
