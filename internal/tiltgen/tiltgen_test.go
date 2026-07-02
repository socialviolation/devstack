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

	out, err := Generate(rw, Options{ManagedEnv: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	t.Log("\n" + out)

	checks := []string{
		`"api"`,
		"cmd=\"fuser -k 8080/tcp\"",
		// serve_cmd sources ./.envrc by default (./ so dash loads it from cwd, not $PATH)
		`[ -f './.envrc' ] && set -a && . './.envrc' && set +a; dotnet run`,
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

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
