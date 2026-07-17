package tiltgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func TestGenerate(t *testing.T) {
	dir := t.TempDir()
	apiDir := filepath.Join(dir, "repos", "api")
	feDir := filepath.Join(dir, "repos", "frontend")

	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/api, ./repos/frontend]
env:
  values:
    OTEL_EXPORTER_OTLP_PROTOCOL: grpc
groups:
  backend: [api]
  web: [frontend]
dependencies:
  frontend: [api]
`)
	write(t, filepath.Join(apiDir, config.ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  workDir: src/App
  run: { command: dotnet run }
  prep: { command: fuser -k 8080/tcp }
  healthcheck: { type: exec, command: 'curl -sf http://localhost:8080/', failureThreshold: 12 }
ports: { http: 8080 }
env:
  values: { OTEL_SERVICE_NAME: api }
links:
  - { url: "http://localhost:8080", label: API }
`)
	write(t, filepath.Join(feDir, config.ServiceManifestFileName), `version: 1
service:
  name: frontend
runtime:
  run: { command: npm start }
  triggerMode: auto
  autoStart: true
  healthcheck: { type: http, port: 4200, path: /health }
ports: { http: 4200 }
`)

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}

	out, err := Generate(rw, Options{ManagedEnv: map[string]map[string]string{
		"api": {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317"},
	}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	t.Log("\n" + out)

	checks := []string{
		`"api"`,
		"cmd=\"fuser -k 8080/tcp\"",
		// serve_cmd is the run command alone: all env now flows through serve_env
		`serve_cmd="dotnet run"`,
		filepath.Join(apiDir, "src/App"), // serve_dir with workDir
		// merged env: managed + workspace + service, sorted
		`"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317"`,
		`"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"`,
		`"OTEL_SERVICE_NAME": "api"`,
		`labels=["backend"]`,    // group → label
		`resource_deps=["api"]`, // dependency → resource_deps
		`trigger_mode=TRIGGER_MODE_AUTO`,
		`auto_init=True`,
		`readiness_probe=probe(exec=exec_action(["bash", "-c", "curl -sf http://localhost:8080/"]), period_secs=5, failure_threshold=12)`,
		`readiness_probe=probe(http_get=http_get_action(port=4200, path="/health"), period_secs=5, failure_threshold=10)`,
		`links=[link("http://localhost:8080", "API")]`,
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("generated Tiltfile missing:\n  %s", c)
		}
	}

	// frontend has no groups membership? it does (web) — verify label present
	if !strings.Contains(out, `labels=["web"]`) {
		t.Errorf("frontend should be labelled web")
	}
}

func TestGenerateAbsoluteWorkDir(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "repos", "svc")
	absWork := t.TempDir()

	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/svc]
`)
	write(t, filepath.Join(svcDir, config.ServiceManifestFileName), `version: 1
service:
  name: svc
runtime:
  workDir: `+absWork+`
  run: { command: ./bin/svc }
`)

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !strings.Contains(out, "serve_dir=\""+absWork+"\"") {
		t.Errorf("absolute workDir should be used verbatim as serve_dir; got:\n%s", out)
	}
	if strings.Contains(out, filepath.Join(svcDir, absWork)) {
		t.Errorf("absolute workDir must not be joined onto the repo path")
	}
}

// ladderRungs are the six env layers, lowest precedence first. Each sets LADDER
// to its own name, so the value reaching serve_env names the rung that won.
var ladderRungs = []string{"envrc", "ws-files", "svc-files", "ws-values", "svc-values", "managed"}

// buildLadder writes a one-service workspace where LADDER is set on rungs 1..upTo
// (1-indexed, matching ladderRungs) and nowhere above, then generates it.
func buildLadder(t *testing.T, upTo int) string {
	t.Helper()
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "repos", "svc")

	has := func(rung int) bool { return upTo >= rung }

	if has(1) {
		write(t, filepath.Join(svcDir, ".envrc"), "export LADDER=envrc\n")
	}
	if has(2) {
		write(t, filepath.Join(svcDir, "ws.env"), "export LADDER=ws-files\n")
	}
	if has(3) {
		write(t, filepath.Join(svcDir, "svc.env"), "export LADDER=svc-files\n")
	}

	wsEnv := "env:\n  files: [ws.env]\n"
	if has(4) {
		wsEnv += "  values: { LADDER: ws-values }\n"
	}
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/svc]
`+wsEnv)

	svcEnv := "env:\n  files: [svc.env]\n"
	if has(5) {
		svcEnv += "  values: { LADDER: svc-values }\n"
	}
	write(t, filepath.Join(svcDir, config.ServiceManifestFileName), `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
`+svcEnv)

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	opts := Options{}
	if has(6) {
		opts.ManagedEnv = map[string]map[string]string{"svc": {"LADDER": "managed"}}
	}
	out, err := Generate(rw, opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

// TestEnvPrecedenceLadder pins the whole ladder: with rungs 1..N present, rung N
// must win. Knocking off the top rung must hand the key to the one below it.
func TestEnvPrecedenceLadder(t *testing.T) {
	for i, want := range ladderRungs {
		rung := i + 1
		t.Run(want, func(t *testing.T) {
			out := buildLadder(t, rung)
			if !strings.Contains(out, `"LADDER": "`+want+`"`) {
				t.Errorf("with rungs 1..%d present, %q should win; got:\n%s", rung, want, out)
			}
			for _, loser := range ladderRungs {
				if loser == want {
					continue
				}
				if strings.Contains(out, `"LADDER": "`+loser+`"`) {
					t.Errorf("rung %q beat %q", loser, want)
				}
			}
		})
	}
}

// TestManagedEnvBeatsEnvrc is the behaviour flip: .envrc used to be sourced at
// service start and clobbered everything devstack injected. It is now the floor.
func TestManagedEnvBeatsEnvrc(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "repos", "svc")

	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/svc]
`)
	write(t, filepath.Join(svcDir, config.ServiceManifestFileName), `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
`)
	write(t, filepath.Join(svcDir, ".envrc"), "export BACKEND_URL=http://localhost:9999\n")

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{ManagedEnv: map[string]map[string]string{
		"svc": {"BACKEND_URL": "http://localhost:8080"},
	}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !strings.Contains(out, `"BACKEND_URL": "http://localhost:8080"`) {
		t.Errorf("ManagedEnv must beat .envrc; got:\n%s", out)
	}
	if strings.Contains(out, "9999") {
		t.Errorf(".envrc value must not survive; got:\n%s", out)
	}
}

// TestServeCmdHasNoEnvSourcing guards the removal: sourcing .envrc inside the
// started process re-exported over serve_env, which is what inverted the ladder.
func TestServeCmdHasNoEnvSourcing(t *testing.T) {
	out := buildLadder(t, len(ladderRungs))
	for _, banned := range []string{".envrc", "set -a", "set +a", "ws.env", "svc.env"} {
		if strings.Contains(out, banned) {
			t.Errorf("generated Tiltfile must not reference %q; got:\n%s", banned, out)
		}
	}
	if !strings.Contains(out, `serve_cmd="./bin/svc"`) {
		t.Errorf("serve_cmd should be the run command alone; got:\n%s", out)
	}
}

// TestManagedEnvIsPerService pins the widening: one map for all services cannot
// express "backend gets frontend's address".
func TestManagedEnvIsPerService(t *testing.T) {
	dir := t.TempDir()

	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/a, ./repos/b]
`)
	for _, name := range []string{"a", "b"} {
		write(t, filepath.Join(dir, "repos", name, config.ServiceManifestFileName), `version: 1
service:
  name: `+name+`
runtime:
  run: { command: ./bin/`+name+` }
`)
	}

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{ManagedEnv: map[string]map[string]string{
		"a": {"PEER_URL": "http://localhost:1111"},
		"b": {"PEER_URL": "http://localhost:2222"},
	}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	aBlock, bBlock, ok := strings.Cut(out, "# b\n")
	if !ok {
		t.Fatalf("expected a block per service; got:\n%s", out)
	}
	if !strings.Contains(aBlock, `"PEER_URL": "http://localhost:1111"`) || strings.Contains(aBlock, "2222") {
		t.Errorf("service a should get only its own PEER_URL; got:\n%s", aBlock)
	}
	if !strings.Contains(bBlock, `"PEER_URL": "http://localhost:2222"`) || strings.Contains(bBlock, "1111") {
		t.Errorf("service b should get only its own PEER_URL; got:\n%s", bBlock)
	}
}

// TestEnvrcResolvedFromWorkDir pins that .envrc is read from the directory the
// command actually runs in, which is what runtime sourcing did.
func TestEnvrcResolvedFromWorkDir(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "repos", "svc")

	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/svc]
`)
	write(t, filepath.Join(svcDir, config.ServiceManifestFileName), `version: 1
service:
  name: svc
runtime:
  workDir: src/App
  run: { command: ./bin/svc }
`)
	write(t, filepath.Join(svcDir, ".envrc"), "export WHERE=repo-root\n")
	write(t, filepath.Join(svcDir, "src", "App", ".envrc"), "export WHERE=work-dir\n")

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !strings.Contains(out, `"WHERE": "work-dir"`) {
		t.Errorf(".envrc should resolve from serve_dir (the workDir); got:\n%s", out)
	}
	if strings.Contains(out, `"WHERE": "repo-root"`) {
		t.Errorf("the repo-root .envrc must not be used when workDir is set; got:\n%s", out)
	}
}

// TestAwkwardValuesSurviveIntoServeEnv: these used to travel through shell
// sourcing and now travel through a Starlark literal.
func TestAwkwardValuesSurviveIntoServeEnv(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "repos", "svc")

	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/svc]
`)
	write(t, filepath.Join(svcDir, config.ServiceManifestFileName), `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
`)
	write(t, filepath.Join(svcDir, ".envrc"), "export TRICKY='a=b\nc=d'\n")

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !strings.Contains(out, `"TRICKY": "a=b\nc=d"`) {
		t.Errorf("multi-line, =-containing value should round-trip escaped; got:\n%s", out)
	}
	if strings.Contains(out, "\nc=d'") {
		t.Errorf("raw newline leaked into the Tiltfile; got:\n%s", out)
	}
}

// TestGenerateFailsOnBrokenEnvrc: the old `;` swallowed sourcing failures.
func TestGenerateFailsOnBrokenEnvrc(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "repos", "svc")

	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/svc]
`)
	write(t, filepath.Join(svcDir, config.ServiceManifestFileName), `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
`)
	write(t, filepath.Join(svcDir, ".envrc"), "export SECRET_TOKEN=hunter2\nif then fi(\n")

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{})
	if err == nil {
		t.Fatalf("a broken .envrc must fail generation; got:\n%s", out)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error must never carry an env value: %v", err)
	}
}

func resourceDepsFor(t *testing.T, edges string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"api", "worker", "db"} {
		write(t, filepath.Join(dir, "repos", name, config.ServiceManifestFileName), `version: 1
service:
  name: `+name+`
runtime:
  run: { command: ./bin/`+name+` }
`)
	}
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/api, ./repos/worker, ./repos/db]
`+edges)

	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

// TestResourceDepsLegacyDependencies pins the regression: a manifest that only
// uses the legacy dependencies: field emits exactly the resource_deps it did
// before calls/startsAfter existed.
func TestResourceDepsLegacyDependencies(t *testing.T) {
	out := resourceDepsFor(t, `dependencies:
  api: [worker]
`)
	if !strings.Contains(out, `resource_deps=["worker"]`) {
		t.Errorf("legacy dependencies must still drive resource_deps; got:\n%s", out)
	}
}

// TestResourceDepsCallsOnly proves a calls: edge alone produces resource_deps —
// a called service must be up before its caller, so calls fold into ordering.
func TestResourceDepsCallsOnly(t *testing.T) {
	out := resourceDepsFor(t, `calls:
  api: [worker]
`)
	if !strings.Contains(out, `resource_deps=["worker"]`) {
		t.Errorf("calls must drive resource_deps; got:\n%s", out)
	}
}

// TestResourceDepsUnionDeduped pins that startsAfter and calls on one service
// merge into a single deduped, sorted resource_deps list.
func TestResourceDepsUnionDeduped(t *testing.T) {
	out := resourceDepsFor(t, `startsAfter:
  api: [worker, db]
calls:
  api: [worker]
`)
	if !strings.Contains(out, `resource_deps=["db", "worker"]`) {
		t.Errorf("resource_deps should be the deduped union of startsAfter and calls; got:\n%s", out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
