package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifestWorkspaceConfig(t *testing.T) {
	workspaceDir := t.TempDir()
	apiDir := filepath.Join(workspaceDir, "repos", "api")
	workerDir := filepath.Join(workspaceDir, "repos", "worker")

	mustWriteFile(t, filepath.Join(workspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
      - ./repos/worker
groups:
  backend:
    - api
    - worker
dependencies:
  api:
    - worker
`)
	mustWriteFile(t, filepath.Join(apiDir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
ports:
  http: 8080
`)
	mustWriteFile(t, filepath.Join(workerDir, ServiceManifestFileName), `version: 1
service:
  name: worker
runtime:
  run:
    command: go run ./cmd/worker
`)

	cfg, err := Load(workspaceDir)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if got, want := cfg.ServicePaths["api"], filepath.Clean(apiDir); got != want {
		t.Fatalf("api path = %q, want %q", got, want)
	}
	if got, want := cfg.ServicePaths["worker"], filepath.Clean(workerDir); got != want {
		t.Fatalf("worker path = %q, want %q", got, want)
	}
	if got := cfg.Groups["backend"]; len(got) != 2 || got[0] != "api" || got[1] != "worker" {
		t.Fatalf("backend group = %#v", got)
	}
	if got := cfg.Deps["api"]; len(got) != 1 || got[0] != "worker" {
		t.Fatalf("api deps = %#v", got)
	}
}

func TestManifestExtendedFields(t *testing.T) {
	workspaceDir := t.TempDir()
	apiDir := filepath.Join(workspaceDir, "repos", "api")

	mustWriteFile(t, filepath.Join(workspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
env:
  values:
    OTEL_EXPORTER_OTLP_ENDPOINT: http://localhost:4317
    DATABASE_HOST: localhost
groups:
  backend:
    - api
`)
	mustWriteFile(t, filepath.Join(apiDir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  workDir: src/App
  run:
    command: dotnet run
  prep:
    command: fuser -k 8080/tcp
  triggerMode: auto
  autoStart: true
  watch:
    - ./bin
  healthcheck:
    type: exec
    command: bash -c "curl -sf http://localhost:8080/"
    periodSecs: 5
    failureThreshold: 12
ports:
  http: 8080
env:
  values:
    OTEL_SERVICE_NAME: api
links:
  - url: http://localhost:8080
    label: API
`)

	rw, err := ResolveWorkspace(workspaceDir)
	if err != nil {
		t.Fatalf("ResolveWorkspace(): %v", err)
	}

	if got := rw.Manifest.Env.Values["DATABASE_HOST"]; got != "localhost" {
		t.Errorf("workspace env DATABASE_HOST = %q, want localhost", got)
	}

	svc := rw.Services["api"]
	if svc.Manifest == nil {
		t.Fatal("api manifest is nil")
	}
	m := svc.Manifest
	if m.Runtime.Prep.Command != "fuser -k 8080/tcp" {
		t.Errorf("prep = %q", m.Runtime.Prep.Command)
	}
	if m.Runtime.TriggerMode != "auto" || !m.Runtime.AutoStart {
		t.Errorf("triggerMode=%q autoStart=%v", m.Runtime.TriggerMode, m.Runtime.AutoStart)
	}
	if len(m.Runtime.Watch) != 1 || m.Runtime.Watch[0] != "./bin" {
		t.Errorf("watch = %#v", m.Runtime.Watch)
	}
	if m.Runtime.Healthcheck.Type != "exec" || m.Runtime.Healthcheck.FailureThreshold != 12 {
		t.Errorf("healthcheck = %#v", m.Runtime.Healthcheck)
	}
	if m.Env.Values["OTEL_SERVICE_NAME"] != "api" {
		t.Errorf("service env = %#v", m.Env.Values)
	}
	if len(m.Links) != 1 || m.Links[0].URL != "http://localhost:8080" || m.Links[0].Label != "API" {
		t.Errorf("links = %#v", m.Links)
	}
}

func TestResolveWorkspaceScanMode(t *testing.T) {
	workspaceDir := t.TempDir()
	apiDir := filepath.Join(workspaceDir, "services", "api")
	workerDir := filepath.Join(workspaceDir, "services", "worker")

	mustWriteFile(t, filepath.Join(workspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: scan
    roots:
      - ./services
`)
	mustWriteFile(t, filepath.Join(apiDir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)
	mustWriteFile(t, filepath.Join(workerDir, ServiceManifestFileName), `version: 1
service:
  name: worker
runtime:
  run:
    command: go run .
`)

	resolved, err := ResolveWorkspace(workspaceDir)
	if err != nil {
		t.Fatalf("ResolveWorkspace(): %v", err)
	}
	if len(resolved.Services) != 2 {
		t.Fatalf("resolved %d services, want 2", len(resolved.Services))
	}
	if _, ok := resolved.Services["api"]; !ok {
		t.Fatalf("api service missing from resolved workspace")
	}
	if _, ok := resolved.Services["worker"]; !ok {
		t.Fatalf("worker service missing from resolved workspace")
	}
}

func TestLegacyWorkspaceManifestAdapter(t *testing.T) {
	workspaceDir := t.TempDir()
	apiDir := filepath.Join(workspaceDir, "api")
	workerDir := filepath.Join(workspaceDir, "worker")

	mustWriteFile(t, filepath.Join(workspaceDir, configFileName), `{
  "deps": {"api": ["worker"]},
  "groups": {"backend": ["api", "worker"]},
  "service_paths": {
    "api": "`+filepath.ToSlash(apiDir)+`",
    "worker": "`+filepath.ToSlash(workerDir)+`"
  }
}
`)

	cfg, err := loadLegacyConfig(workspaceDir)
	if err != nil {
		t.Fatalf("loadLegacyConfig(): %v", err)
	}
	manifest, err := LegacyWorkspaceManifest(workspaceDir, cfg)
	if err != nil {
		t.Fatalf("LegacyWorkspaceManifest(): %v", err)
	}
	if manifest.Workspace.RepoDiscovery.Mode != RepoDiscoveryModeExplicit {
		t.Fatalf("repo discovery mode = %q", manifest.Workspace.RepoDiscovery.Mode)
	}
	if got := manifest.Dependencies["api"]; len(got) != 1 || got[0] != "worker" {
		t.Fatalf("dependencies = %#v", manifest.Dependencies)
	}
	if len(manifest.Workspace.RepoDiscovery.Repos) != 2 {
		t.Fatalf("repos = %#v", manifest.Workspace.RepoDiscovery.Repos)
	}
}

func TestValidateManifestFailures(t *testing.T) {
	workspaceDir := t.TempDir()
	badWorkspaceDir := filepath.Join(workspaceDir, "bad")
	dupeWorkspaceDir := filepath.Join(workspaceDir, "dupe")
	alphaDir := filepath.Join(dupeWorkspaceDir, "services", "alpha")
	betaDir := filepath.Join(dupeWorkspaceDir, "services", "beta")

	mustWriteFile(t, filepath.Join(badWorkspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: broken
  repoDiscovery:
    mode: explicit
`)
	if _, err := LoadWorkspaceManifest(badWorkspaceDir); err == nil {
		t.Fatal("LoadWorkspaceManifest() succeeded for malformed explicit manifest")
	}

	mustWriteFile(t, filepath.Join(dupeWorkspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: dupe
  repoDiscovery:
    mode: explicit
    repos:
      - ./services/alpha
      - ./services/beta
`)
	mustWriteFile(t, filepath.Join(alphaDir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)
	mustWriteFile(t, filepath.Join(betaDir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)

	if _, err := ResolveWorkspace(dupeWorkspaceDir); err == nil {
		t.Fatal("ResolveWorkspace() succeeded for duplicate service names")
	}
}

func TestResolveIdentityFromWorkspaceRootAndRepoCwd(t *testing.T) {
	workspaceDir := t.TempDir()
	apiDir := filepath.Join(workspaceDir, "services", "api")
	apiNestedDir := filepath.Join(apiDir, "internal", "handlers")

	mustWriteFile(t, filepath.Join(workspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./services/api
`)
	mustWriteFile(t, filepath.Join(apiDir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)
	if err := os.MkdirAll(apiNestedDir, 0755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}

	fromWorkspace, err := ResolveIdentity(workspaceDir)
	if err != nil {
		t.Fatalf("ResolveIdentity(workspace root): %v", err)
	}
	if fromWorkspace.WorkspaceName != "playground" {
		t.Fatalf("workspace name = %q, want playground", fromWorkspace.WorkspaceName)
	}
	if fromWorkspace.ServiceName != "" {
		t.Fatalf("workspace root resolved service %q, want empty", fromWorkspace.ServiceName)
	}

	fromRepo, err := ResolveIdentity(apiNestedDir)
	if err != nil {
		t.Fatalf("ResolveIdentity(repo cwd): %v", err)
	}
	if fromRepo.WorkspaceRoot != filepath.Clean(workspaceDir) {
		t.Fatalf("workspace root = %q, want %q", fromRepo.WorkspaceRoot, filepath.Clean(workspaceDir))
	}
	if fromRepo.ServiceName != "api" {
		t.Fatalf("service name = %q, want api", fromRepo.ServiceName)
	}
}

func TestObservabilityEnabledResolution(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	cases := []struct {
		name        string
		obs         WorkspaceManifestObservability
		wantEnabled bool
		wantBackend string
	}{
		{
			name:        "empty defaults off",
			obs:         WorkspaceManifestObservability{},
			wantEnabled: false,
			wantBackend: "",
		},
		{
			name:        "explicit enabled true, no backend defaults signoz",
			obs:         WorkspaceManifestObservability{Enabled: boolPtr(true)},
			wantEnabled: true,
			wantBackend: "signoz",
		},
		{
			name:        "explicit enabled false wins over backend",
			obs:         WorkspaceManifestObservability{Enabled: boolPtr(false), Backend: "signoz"},
			wantEnabled: false,
			wantBackend: "",
		},
		{
			name:        "inferred from local.enabled",
			obs:         WorkspaceManifestObservability{Local: WorkspaceManifestObservabilityLocal{Enabled: true}},
			wantEnabled: true,
			wantBackend: "signoz",
		},
		{
			name:        "inferred from backend",
			obs:         WorkspaceManifestObservability{Backend: "grafana"},
			wantEnabled: true,
			wantBackend: "grafana",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.obs.IsEnabled(); got != tc.wantEnabled {
				t.Errorf("IsEnabled() = %v, want %v", got, tc.wantEnabled)
			}
			if got := tc.obs.ResolvedBackend(); got != tc.wantBackend {
				t.Errorf("ResolvedBackend() = %q, want %q", got, tc.wantBackend)
			}
		})
	}
}

func TestObservabilityEnabledFromWorkspace(t *testing.T) {
	// Manifest with no observability block → disabled.
	off := t.TempDir()
	mustWriteFile(t, filepath.Join(off, WorkspaceManifestFileName), `version: 1
workspace:
  name: off-ws
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
`)
	mustWriteFile(t, filepath.Join(off, "api", ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)
	if ObservabilityEnabled(off) {
		t.Errorf("ObservabilityEnabled(no block) = true, want false")
	}

	// Manifest that opts in via observability.enabled → enabled.
	on := t.TempDir()
	mustWriteFile(t, filepath.Join(on, WorkspaceManifestFileName), `version: 1
workspace:
  name: on-ws
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
observability:
  enabled: true
`)
	mustWriteFile(t, filepath.Join(on, "api", ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)
	if !ObservabilityEnabled(on) {
		t.Errorf("ObservabilityEnabled(enabled) = false, want true")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
