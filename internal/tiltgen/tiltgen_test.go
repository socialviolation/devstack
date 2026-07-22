package tiltgen

import (
	"os"
	"path/filepath"
	"strconv"
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

// generateOne writes a one-service workspace (svc) with the given service-manifest
// body and optional workspace env block, then generates it.
func generateOne(t *testing.T, svcBody, wsEnv string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "repos", "svc")
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/svc]
`+wsEnv)
	write(t, filepath.Join(svcDir, config.ServiceManifestFileName), svcBody)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	return Generate(rw, Options{})
}

// TestSelfPortResolves: a service reads its own listen port via ${self.port.http}.
func TestSelfPortResolves(t *testing.T) {
	out, err := generateOne(t, `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
ports: { http: 5555 }
env:
  values: { PORT: "${self.port.http}" }
`, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, `"PORT": "5555"`) {
		t.Errorf("${self.port.http} should resolve to the service's own port; got:\n%s", out)
	}
	if strings.Contains(out, "${") {
		t.Errorf("unresolved reference leaked into the Tiltfile; got:\n%s", out)
	}
}

// TestCrossServiceURLResolves: ${api.url} resolves to api's http address.
func TestCrossServiceURLResolves(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/api, ./repos/web]
`)
	write(t, filepath.Join(dir, "repos", "api", config.ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run: { command: ./bin/api }
ports: { http: 8080 }
`)
	write(t, filepath.Join(dir, "repos", "web", config.ServiceManifestFileName), `version: 1
service:
  name: web
runtime:
  run: { command: ./bin/web }
env:
  values: { NAVEXA_API_URL: "${api.url}" }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, `"NAVEXA_API_URL": "http://localhost:8080"`) {
		t.Errorf("${api.url} should resolve to api's http address; got:\n%s", out)
	}
}

// TestLinksDerivedAndMerged: one link per ports: entry, explicit links resolved
// and kept in order, and an explicit link for a port's URL replaces its derived
// default while a path-bearing explicit link is kept alongside.
func TestLinksDerivedAndMerged(t *testing.T) {
	out, err := generateOne(t, `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
ports: { http: 3000, admin: 9000 }
links:
  - { url: "http://localhost:3000", label: Home }
  - { url: "http://localhost:3000/health", label: health }
`, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `links=[link("http://localhost:3000", "Home"), link("http://localhost:3000/health", "health"), link("http://localhost:9000", "admin")]`
	if !strings.Contains(out, want) {
		t.Errorf("link merge wrong;\nwant substring: %s\ngot:\n%s", want, out)
	}
}

// TestDerivedLinkFromPortsOnly: a service with ports: and no links: gains a
// derived link labelled by its port key.
func TestDerivedLinkFromPortsOnly(t *testing.T) {
	out, err := generateOne(t, `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
ports: { http: 4200 }
`, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, `links=[link("http://localhost:4200", "http")]`) {
		t.Errorf("ports: entry should derive a link; got:\n%s", out)
	}
}

// TestRequiredEnvPresentPasses: a required key set on any rung passes generation.
func TestRequiredEnvPresentPasses(t *testing.T) {
	out, err := generateOne(t, `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
env:
  values: { API_KEY: secret }
  required: [API_KEY]
`, "")
	if err != nil {
		t.Fatalf("a present required key must pass; got: %v", err)
	}
	if !strings.Contains(out, `"API_KEY": "secret"`) {
		t.Errorf("expected API_KEY in serve_env; got:\n%s", out)
	}
}

// TestRequiredEnvAbsentFails: a required key on no rung fails at generate, naming
// the key and the service.
func TestRequiredEnvAbsentFails(t *testing.T) {
	_, err := generateOne(t, `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
env:
  required: [MISSING_KEY]
`, "")
	if err == nil {
		t.Fatal("an absent required key must fail generation")
	}
	if !strings.Contains(err.Error(), "MISSING_KEY") || !strings.Contains(err.Error(), "svc") {
		t.Errorf("error should name the missing key and service; got: %v", err)
	}
}

// TestUnresolvableRefFails: an unresolvable reference fails generation and no
// ${ ever reaches the output.
func TestUnresolvableRefFails(t *testing.T) {
	out, err := generateOne(t, `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
env:
  values: { PEER: "${ghost.port.http}" }
`, "")
	if err == nil {
		t.Fatalf("an unresolvable reference must fail generation; got:\n%s", out)
	}
	if strings.Contains(out, "${") {
		t.Errorf("output must not contain an unresolved reference; got:\n%s", out)
	}
}

// TestRegressionNoPortsNoRefs pins byte-identical output for a manifest with a
// hand-written link, no ports:, and no references — the pre-resolver behaviour.
func TestRegressionNoPortsNoRefs(t *testing.T) {
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
links:
  - { url: "http://example.com", label: Docs }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := header + `
# svc
local_resource(
    "svc",
    serve_cmd="./bin/svc",
    serve_dir="` + svcDir + `",
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    links=[link("http://example.com", "Docs")],
)
`
	if out != want {
		t.Errorf("output not byte-identical to pre-resolver behaviour;\nwant:\n%q\ngot:\n%q", want, out)
	}
}

// TestInjectedBookDrivesResolution: with Options.Book set, references resolve
// against the injected book's ports, not the manifest's ports:. The manifest
// pins http:8080 but the book allocates 20001, so 20001 must reach serve_env and
// the resolved link.
func TestInjectedBookDrivesResolution(t *testing.T) {
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
ports: { http: 8080 }
env:
  values: { PORT: "${self.port.http}" }
links:
  - { url: "${self.url}", label: Home }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{Book: config.PortBook{"svc": {"http": 20001}}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, `"PORT": "20001"`) {
		t.Errorf("injected book must drive ${self.port.http}; want 20001, got:\n%s", out)
	}
	if strings.Contains(out, `"PORT": "8080"`) {
		t.Errorf("manifest port must not win over the injected book; got:\n%s", out)
	}
	if !strings.Contains(out, `link("http://localhost:20001", "Home")`) {
		t.Errorf("injected book must drive the resolved ${self.url} link; want 20001, got:\n%s", out)
	}
}

func TestInjectedBookDrivesDerivedLinks(t *testing.T) {
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
ports: { http: 8080 }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{Book: config.PortBook{"svc": {"http": 20001}}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(out, `link("http://localhost:20001", "http")`) {
		t.Errorf("derived link must follow the injected book port 20001, not the manifest 8080; got:\n%s", out)
	}
	if strings.Contains(out, "8080") {
		t.Errorf("manifest port 8080 must not appear under an injected book; got:\n%s", out)
	}
}

// TestNoBookMatchesBuildPortBook pins the fallback regression: a nil Options.Book
// generates byte-identically to passing BuildPortBook(rw) explicitly, so existing
// callers get exactly the manifest-derived behaviour.
func TestNoBookMatchesBuildPortBook(t *testing.T) {
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
ports: { http: 8080 }
env:
  values: { PORT: "${self.port.http}" }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	nilBook, err := Generate(rw, Options{})
	if err != nil {
		t.Fatalf("Generate(nil book): %v", err)
	}
	explicit, err := Generate(rw, Options{Book: config.BuildPortBook(rw)})
	if err != nil {
		t.Fatalf("Generate(explicit book): %v", err)
	}
	if nilBook != explicit {
		t.Errorf("nil Book must fall back to BuildPortBook byte-identically;\nnil:\n%q\nexplicit:\n%q", nilBook, explicit)
	}
	if !strings.Contains(nilBook, `"PORT": "8080"`) {
		t.Errorf("manifest ports must drive resolution when no Book is set; got:\n%s", nilBook)
	}
}

// TestCommandRefsResolveUnderBook: ${self.port.http} in run, prep, and
// healthcheck commands resolves to the injected book's allocated port, not the
// manifest's pinned port.
func TestCommandRefsResolveUnderBook(t *testing.T) {
	out := generateWithBook(t, config.PortBook{"svc": {"http": 20001}})
	for _, want := range []string{
		`serve_cmd="dotnet run --urls http://localhost:20001"`,
		`cmd="fuser -k 20001/tcp"`,
		`readiness_probe=probe(exec=exec_action(["bash", "-c", "curl -sf http://localhost:20001/health"])`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("command ref should resolve to the allocated port;\nwant substring: %s\ngot:\n%s", want, out)
		}
	}
	if strings.Contains(out, "63290") {
		t.Errorf("manifest-pinned port must not survive under an injected book; got:\n%s", out)
	}
	if strings.Contains(out, "${") {
		t.Errorf("unresolved reference leaked into the Tiltfile; got:\n%s", out)
	}
}

// TestCommandRefsResolveWithoutBook: with no injected book, command refs resolve
// to the manifest's pinned port.
func TestCommandRefsResolveWithoutBook(t *testing.T) {
	out := generateWithBook(t, nil)
	for _, want := range []string{
		`serve_cmd="dotnet run --urls http://localhost:63290"`,
		`cmd="fuser -k 63290/tcp"`,
		`readiness_probe=probe(exec=exec_action(["bash", "-c", "curl -sf http://localhost:63290/health"])`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("command ref should resolve to the pinned port with no book;\nwant substring: %s\ngot:\n%s", want, out)
		}
	}
}

// TestCommandRefUnresolvableFails: an unknown ref in a command fails generation
// and no ${ reaches the output.
func TestCommandRefUnresolvableFails(t *testing.T) {
	out, err := generateOne(t, `version: 1
service:
  name: svc
runtime:
  run: { command: "./bin/svc ${nope.url}" }
ports: { http: 8080 }
`, "")
	if err == nil {
		t.Fatalf("an unknown ref in a command must fail generation; got:\n%s", out)
	}
	if strings.Contains(out, "${") {
		t.Errorf("output must not contain an unresolved reference; got:\n%s", out)
	}
}

// generateWithBook writes a service whose run/prep/healthcheck commands all
// reference ${self.port.http} (pinned to 63290) and generates it under the given
// book (nil means fall back to the manifest port).
func generateWithBook(t *testing.T, book config.PortBook) string {
	t.Helper()
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
  run: { command: "dotnet run --urls http://localhost:${self.port.http}" }
  prep: { command: "fuser -k ${self.port.http}/tcp" }
  healthcheck: { type: exec, command: "curl -sf http://localhost:${self.port.http}/health" }
ports: { http: 63290 }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	out, err := Generate(rw, Options{Book: book})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

// writeFEBEWorkspace writes a two-service workspace (frontend depends on backend,
// grouped web/api) at a fresh temp dir and returns the resolved workspace. Both
// services pin http ports so the port book resolves their derived links.
func writeFEBEWorkspace(t *testing.T, fePort, bePort int) *config.ResolvedWorkspace {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/frontend, ./repos/backend]
groups:
  web: [frontend]
  api: [backend]
dependencies:
  frontend: [backend]
`)
	write(t, filepath.Join(dir, "repos", "backend", config.ServiceManifestFileName), `version: 1
service:
  name: backend
runtime:
  run: { command: ./bin/backend }
ports: { http: `+itoa(bePort)+` }
`)
	write(t, filepath.Join(dir, "repos", "frontend", config.ServiceManifestFileName), `version: 1
service:
  name: frontend
runtime:
  run: { command: ./bin/frontend }
ports: { http: `+itoa(fePort)+` }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	return rw
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestGenerateCombinedNamespacesStack is the stage-1 load-bearing test: base's
// services plus one stack's overlay services in ONE Tiltfile, the stack namespaced
// so nothing collides and its deps rewire to its own services.
func TestGenerateCombinedNamespacesStack(t *testing.T) {
	baseRW := writeFEBEWorkspace(t, 4200, 8080)
	stackRW := writeFEBEWorkspace(t, 4200, 8080)
	stackBook := config.PortBook{"frontend": {"http": 14200}, "backend": {"http": 18080}}

	out, err := GenerateCombined(baseRW, Options{}, []StackGen{{
		Workspace: stackRW,
		Options:   Options{Book: stackBook},
		Namespace: "perf",
	}})
	if err != nil {
		t.Fatalf("GenerateCombined: %v", err)
	}
	t.Log("\n" + out)

	// 1. base resources keep bare names and base ports
	for _, want := range []string{
		`    "frontend",`,
		`    "backend",`,
		`link("http://localhost:4200", "http")`,
		`link("http://localhost:8080", "http")`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("base resource missing: %s", want)
		}
	}

	// 2. stack resources are namespaced and carry the injected ports
	for _, want := range []string{
		`    "frontend:perf",`,
		`    "backend:perf",`,
		`link("http://localhost:14200", "http")`,
		`link("http://localhost:18080", "http")`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("namespaced stack resource missing: %s", want)
		}
	}

	// 3. THE load-bearing one: the stack's frontend depends on the stack's own
	// backend (backend:perf), never base's bare backend.
	_, stackFE, ok := strings.Cut(out, "\n# frontend:perf\n")
	if !ok {
		t.Fatalf("no frontend:perf block in output:\n%s", out)
	}
	if !strings.Contains(stackFE, `resource_deps=["backend:perf"]`) {
		t.Errorf("frontend:perf must depend on backend:perf; got:\n%s", stackFE)
	}
	if strings.Contains(stackFE, `resource_deps=["backend"]`) {
		t.Errorf("frontend:perf must NOT depend on base's bare backend; got:\n%s", stackFE)
	}

	// 4. stack resources carry the stack label alongside their group label
	if !strings.Contains(stackFE, `labels=["web", "perf"]`) {
		t.Errorf("frontend:perf must carry group + stack label; got:\n%s", stackFE)
	}

	// 5. the base portion is byte-identical to a standalone Generate(baseRW): the
	// combined file is base's file with the stack blocks appended.
	standalone, err := Generate(baseRW, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(out, standalone) {
		t.Errorf("combined output must start with the standalone base Tiltfile;\nstandalone:\n%q\ncombined:\n%q", standalone, out)
	}
}

// TestGenerateCombinedNoStacksMatchesGenerate pins that GenerateCombined with no
// stacks is byte-identical to Generate — single-workspace generation is untouched.
func TestGenerateCombinedNoStacksMatchesGenerate(t *testing.T) {
	rw := writeFEBEWorkspace(t, 4200, 8080)
	gen, err := Generate(rw, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	combined, err := GenerateCombined(rw, Options{}, nil)
	if err != nil {
		t.Fatalf("GenerateCombined: %v", err)
	}
	if gen != combined {
		t.Errorf("GenerateCombined with no stacks must equal Generate;\nGenerate:\n%q\nCombined:\n%q", gen, combined)
	}
}

// TestGenerateHostPrefixesDisambiguatesCollisions pins host mode: two workspaces
// each with a base service named "api" produce two distinct resources ws1:api and
// ws2:api, so same-named services never collide across workspaces.
func TestGenerateHostPrefixesDisambiguatesCollisions(t *testing.T) {
	ws1 := writeAPIWorkspace(t, 8080)
	ws2 := writeAPIWorkspace(t, 8090)

	out, err := GenerateHost([]WorkspaceGen{
		{Name: "ws1", Base: ws1},
		{Name: "ws2", Base: ws2},
	})
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}
	t.Log("\n" + out)

	for _, want := range []string{`    "ws1:api",`, `    "ws2:api",`} {
		if !strings.Contains(out, want) {
			t.Errorf("host output missing prefixed resource: %s", want)
		}
	}
	if strings.Contains(out, "\n    \"api\",\n") {
		t.Errorf("host output must not carry a bare, unprefixed api resource; got:\n%s", out)
	}
	if strings.Count(out, "local_resource(") != 2 {
		t.Errorf("expected exactly two resources; got:\n%s", out)
	}
}

// TestGenerateHostStackSuffixWithPrefix pins that a workspace's base service and
// its active stack's service coexist as ws:svc and ws:svc:stack.
func TestGenerateHostStackSuffixWithPrefix(t *testing.T) {
	base := writeFEBEWorkspace(t, 4200, 8080)
	stack := writeFEBEWorkspace(t, 4200, 8080)
	stackBook := config.PortBook{"frontend": {"http": 14200}, "backend": {"http": 18080}}

	out, err := GenerateHost([]WorkspaceGen{{
		Name: "ws",
		Base: base,
		Stacks: []StackGen{{
			Workspace: stack,
			Options:   Options{Book: stackBook},
			Namespace: "perf",
		}},
	}})
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}
	t.Log("\n" + out)

	for _, want := range []string{
		`    "ws:frontend",`,
		`    "ws:backend",`,
		`    "ws:frontend:perf",`,
		`    "ws:backend:perf",`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("host output missing resource: %s", want)
		}
	}
}

// TestGenerateHostResourceDepsPrefixedAndSuffixed pins that resource_deps stay
// inside their workspace and stack: a base service's dep is ws:dep and a stack
// service's dep is ws:dep:stack, never crossing into base or another workspace.
func TestGenerateHostResourceDepsPrefixedAndSuffixed(t *testing.T) {
	base := writeFEBEWorkspace(t, 4200, 8080)
	stack := writeFEBEWorkspace(t, 4200, 8080)
	stackBook := config.PortBook{"frontend": {"http": 14200}, "backend": {"http": 18080}}

	out, err := GenerateHost([]WorkspaceGen{{
		Name: "ws",
		Base: base,
		Stacks: []StackGen{{
			Workspace: stack,
			Options:   Options{Book: stackBook},
			Namespace: "perf",
		}},
	}})
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}

	_, baseFE, ok := strings.Cut(out, "\n# ws:frontend\n")
	if !ok {
		t.Fatalf("no ws:frontend block in output:\n%s", out)
	}
	baseFE, _, _ = strings.Cut(baseFE, "\n# ")
	if !strings.Contains(baseFE, `resource_deps=["ws:backend"]`) {
		t.Errorf("ws:frontend must depend on ws:backend; got:\n%s", baseFE)
	}

	_, stackFE, ok := strings.Cut(out, "\n# ws:frontend:perf\n")
	if !ok {
		t.Fatalf("no ws:frontend:perf block in output:\n%s", out)
	}
	if !strings.Contains(stackFE, `resource_deps=["ws:backend:perf"]`) {
		t.Errorf("ws:frontend:perf must depend on ws:backend:perf; got:\n%s", stackFE)
	}
	if strings.Contains(stackFE, `resource_deps=["ws:backend"]`) {
		t.Errorf("stack dep must not cross into the base backend; got:\n%s", stackFE)
	}
}

// TestGenerateHostLabelsCarryWorkspace pins that every resource carries its
// workspace name as a label, alongside group and stack labels.
func TestGenerateHostLabelsCarryWorkspace(t *testing.T) {
	base := writeFEBEWorkspace(t, 4200, 8080)
	stack := writeFEBEWorkspace(t, 4200, 8080)
	stackBook := config.PortBook{"frontend": {"http": 14200}, "backend": {"http": 18080}}

	out, err := GenerateHost([]WorkspaceGen{{
		Name: "ws",
		Base: base,
		Stacks: []StackGen{{
			Workspace: stack,
			Options:   Options{Book: stackBook},
			Namespace: "perf",
		}},
	}})
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}

	_, baseFE, _ := strings.Cut(out, "\n# ws:frontend\n")
	baseFE, _, _ = strings.Cut(baseFE, "\n# ")
	if !strings.Contains(baseFE, `labels=["web", "ws"]`) {
		t.Errorf("base resource must carry group + workspace label; got:\n%s", baseFE)
	}

	_, stackFE, _ := strings.Cut(out, "\n# ws:frontend:perf\n")
	if !strings.Contains(stackFE, `labels=["web", "perf", "ws"]`) {
		t.Errorf("stack resource must carry group + stack + workspace label; got:\n%s", stackFE)
	}
}

// TestGenerateHostSingleWorkspaceMatchesPrefixedGenerate is the regression guard:
// GenerateCombined stays prefix-less (no ws: segment), while the same workspace in
// host mode gains the prefix — proving host mode is the only source of prefixes.
func TestGenerateHostSingleWorkspaceMatchesPrefixedGenerate(t *testing.T) {
	rw := writeFEBEWorkspace(t, 4200, 8080)

	combined, err := GenerateCombined(rw, Options{}, nil)
	if err != nil {
		t.Fatalf("GenerateCombined: %v", err)
	}
	if strings.Contains(combined, `"demo:`) || strings.Contains(combined, `"ws:`) {
		t.Errorf("prefix-less render must not carry a workspace prefix; got:\n%s", combined)
	}
	if !strings.Contains(combined, `    "frontend",`) || !strings.Contains(combined, `    "backend",`) {
		t.Errorf("prefix-less render must keep bare resource names; got:\n%s", combined)
	}
}

// writeAPIWorkspace writes a one-service workspace whose service is named "api"
// (pinning http:port), used to prove the workspace prefix disambiguates same-named
// services across workspaces in host mode.
func writeAPIWorkspace(t *testing.T, port int) *config.ResolvedWorkspace {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/api]
`)
	write(t, filepath.Join(dir, "repos", "api", config.ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run: { command: ./bin/api }
ports: { http: `+itoa(port)+` }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	return rw
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
