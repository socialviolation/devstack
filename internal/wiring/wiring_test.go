package wiring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (*config.ResolvedWorkspace, []string, map[string][]string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: shop
  repoDiscovery:
    mode: explicit
    repos: [./web, ./api, ./worker]
dependencies:
  web: [api, worker]
  api: [worker]
`)
	write(t, filepath.Join(dir, "web", config.ServiceManifestFileName), `version: 1
service:
  name: web
runtime:
  run:
    command: npm start
ports:
  http: 5000
env:
  values:
    API_URL: ${api.url}/v1
    ORDERS_QUEUE: orders${stack.suffix:worker}
    LEGACY_WORKER: http://localhost:5002
    NOT_A_PORT: localhost:50021
    PEEK: localhost:20002
    GHOST_URL: ${ghost.url}
    SELF_URL: ${self.url}
`)
	write(t, filepath.Join(dir, "api", config.ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
ports:
  http: 5001
env:
  values:
    CALLBACK: ${web.url}/hook
`)
	write(t, filepath.Join(dir, "worker", config.ServiceManifestFileName), `version: 1
service:
  name: worker
runtime:
  run:
    command: go run ./worker
ports:
  http: 5002
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	services := []string{"api", "web", "worker"}
	deps := map[string][]string{}
	for _, s := range services {
		deps[s] = rw.Manifest.ResourceDeps(s)
	}
	return rw, services, deps
}

func copies(t *testing.T) ([]string, map[string][]string, Copy, *Copy) {
	t.Helper()
	rw, services, deps := fixture(t)
	env, err := RawEnv(rw, "")
	if err != nil {
		t.Fatal(err)
	}
	book := config.BuildPortBook(rw)
	base := Copy{Label: "base", Env: env, Book: book}

	stackWeb := map[string]string{}
	for k, v := range env["web"] {
		if k != "PEEK" {
			stackWeb[k] = v
		}
	}
	stack := &Copy{
		Label: "feat",
		Env:   map[string]map[string]string{"web": stackWeb, "worker": env["worker"]},
		Book:  config.MergeStackBook(book, config.PortBook{"web": {"http": 20000}, "worker": {"http": 20002}}),
		Scope: config.StackScope{Name: "feat", Services: []string{"web", "worker"}},
	}
	return services, deps, base, stack
}

func find(t *testing.T, m Map, from, key, to string) Link {
	t.Helper()
	for _, l := range m.Links {
		if l.From == from && l.Key == key && l.To == to {
			return l
		}
	}
	t.Fatalf("no link %s.%s → %s in %+v", from, key, to, m.Links)
	return Link{}
}

func TestDeriveReferenceEdges(t *testing.T) {
	services, deps, base, stack := copies(t)
	m := Derive(services, deps, base, stack)

	l := find(t, m, "web", "API_URL", "api")
	if l.Via != "${api.url}" || !l.Declared {
		t.Errorf("API_URL via = %q declared = %v", l.Via, l.Declared)
	}
	if l.Base.Value != "http://localhost:5001/v1" || l.Base.Target != "base" {
		t.Errorf("base hop = %+v", l.Base)
	}
	if l.Stack.Value != "http://localhost:5001/v1" || l.Stack.Target != "base" {
		t.Errorf("stack hop = %+v", l.Stack)
	}
	if got := l.Flag("feat"); got != "stack feat web → base api" {
		t.Errorf("flag = %q", got)
	}
}

func TestDeriveStackSuffixFollowsTheStackCopy(t *testing.T) {
	services, deps, base, stack := copies(t)
	l := find(t, Derive(services, deps, base, stack), "web", "ORDERS_QUEUE", "worker")
	if l.Base.Value != "orders" || l.Base.Target != "base" {
		t.Errorf("base hop = %+v", l.Base)
	}
	if l.Stack.Value != "orders-feat" || l.Stack.Target != "feat" {
		t.Errorf("stack hop = %+v", l.Stack)
	}
	if got := l.Flag("feat"); got != "" {
		t.Errorf("flag = %q, want none", got)
	}
}

func TestDeriveLiteralBasePortInAStackIsFlagged(t *testing.T) {
	services, deps, base, stack := copies(t)
	m := Derive(services, deps, base, stack)
	l := find(t, m, "web", "LEGACY_WORKER", "worker")
	if l.Via != "literal port 5002" || l.Stack.Target != "base" {
		t.Errorf("link = %+v stack = %+v", l, l.Stack)
	}
	if got := l.Flag("feat"); got != "stack feat web → base worker" {
		t.Errorf("flag = %q", got)
	}
	for _, l := range m.Links {
		if l.Key == "NOT_A_PORT" || l.Key == "SELF_URL" {
			t.Errorf("unexpected link %+v", l)
		}
	}
}

func TestDeriveBaseReachingIntoAStackIsFlagged(t *testing.T) {
	services, deps, base, stack := copies(t)
	l := find(t, Derive(services, deps, base, stack), "web", "PEEK", "worker")
	if l.Base.Target != "feat" || l.Stack != nil {
		t.Errorf("base = %+v stack = %+v", l.Base, l.Stack)
	}
	if got := l.Flag("feat"); got != "base web → stack feat worker" {
		t.Errorf("flag = %q", got)
	}
}

func TestDeriveIssues(t *testing.T) {
	services, deps, base, _ := copies(t)
	m := Derive(services, deps, base, nil)

	var codes []string
	for _, is := range m.Issues {
		codes = append(codes, is.Severity+"/"+is.Code+"/"+is.From+"/"+is.To)
	}
	got := strings.Join(codes, ",")
	for _, want := range []string{
		"error/unknown-service/web/ghost",
		"note/no-key/api/worker",
		"note/undeclared-edge/api/web",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("issues %s: missing %s", got, want)
		}
	}
	if strings.Contains(got, "no-key/web/") {
		t.Errorf("web's declared edges have keys, got %s", got)
	}
}

func TestHasPort(t *testing.T) {
	cases := map[string]bool{
		"localhost:5002":         true,
		"http://localhost:5002/": true,
		"5002":                   true,
		"localhost:50021":        false,
		"localhost:15002":        false,
		"x:15002,y:5002":         true,
	}
	for s, want := range cases {
		if got := hasPort(s, 5002); got != want {
			t.Errorf("hasPort(%q) = %v, want %v", s, got, want)
		}
	}
}
