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

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
