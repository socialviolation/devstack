package config

import (
	"path/filepath"
	"testing"
)

func TestResolveContextPrecedenceAndSourceAttribution(t *testing.T) {
	workspaceDir := t.TempDir()
	apiDir := filepath.Join(workspaceDir, "services", "api")
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
  workDir: .
  run:
    command: go run .
env:
  files:
    - .envrc
telemetry:
  traces:
    expected: true
  logs:
    expected: true
  serviceName: api
`)

	ctx, err := ResolveContext(ResolveOptions{
		StartPath:       apiDir,
		WorkspacePath:   workspaceDir,
		EnvironmentName: "staging",
		InvocationEnv: map[string]string{
			"DEVSTACK_WORKSPACE":   "/wrong/workspace",
			"DEVSTACK_ENVIRONMENT": "prod",
		},
		RuntimeOverrides: RuntimeOverrides{
			WorkspacePath:   "/runtime/workspace",
			EnvironmentName: "runtime",
			ServiceName:     "worker",
		},
	})
	if err != nil {
		t.Fatalf("ResolveContext(): %v", err)
	}

	if ctx.WorkspaceRoot.Value != filepath.Clean(workspaceDir) {
		t.Fatalf("workspace root = %q, want %q", ctx.WorkspaceRoot.Value, filepath.Clean(workspaceDir))
	}
	if ctx.WorkspaceRoot.Source != SourceCLIFlag {
		t.Fatalf("workspace root source = %q, want %q", ctx.WorkspaceRoot.Source, SourceCLIFlag)
	}
	if ctx.EnvironmentName.Value != "staging" || ctx.EnvironmentName.Source != SourceCLIFlag {
		t.Fatalf("environment = (%q, %q), want staging from cli", ctx.EnvironmentName.Value, ctx.EnvironmentName.Source)
	}
	if ctx.CurrentService.Value != "worker" || ctx.CurrentService.Source != SourceRuntimeOverride {
		t.Fatalf("current service = (%q, %q), want worker from runtime override", ctx.CurrentService.Value, ctx.CurrentService.Source)
	}
}

func TestResolveServiceConfigFromRepoCwd(t *testing.T) {
	workspaceDir := t.TempDir()
	apiDir := filepath.Join(workspaceDir, "services", "api")
	mustWriteFile(t, filepath.Join(workspaceDir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./services/api
groups:
  backend:
    - api
dependencies:
  api:
    - worker
`)
	mustWriteFile(t, filepath.Join(apiDir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  workDir: .
  run:
    command: go run .
  healthcheck:
    type: http
    url: http://localhost:8080/health
env:
  files:
    - .envrc
telemetry:
  traces:
    expected: true
  logs:
    expected: true
  serviceName: api
`)

	ctx, err := ResolveContext(ResolveOptions{StartPath: apiDir})
	if err != nil {
		t.Fatalf("ResolveContext(): %v", err)
	}
	if ctx.CurrentService.Value != "api" {
		t.Fatalf("current service = %q, want api", ctx.CurrentService.Value)
	}
	if ctx.CurrentService.Source != SourceCWD {
		t.Fatalf("current service source = %q, want %q", ctx.CurrentService.Source, SourceCWD)
	}

	service, err := ResolveServiceConfig(ctx, "")
	if err != nil {
		t.Fatalf("ResolveServiceConfig(): %v", err)
	}
	if service.RunCommand.Value != "go run ." || service.RunCommand.Source != SourceService {
		t.Fatalf("run command = (%q, %q)", service.RunCommand.Value, service.RunCommand.Source)
	}
	if len(service.Groups) != 1 || service.Groups[0].Value != "backend" || service.Groups[0].Source != SourceWorkspace {
		t.Fatalf("groups = %#v", service.Groups)
	}
	if len(service.Dependencies) != 1 || service.Dependencies[0].Value != "worker" || service.Dependencies[0].Source != SourceWorkspace {
		t.Fatalf("dependencies = %#v", service.Dependencies)
	}
	if len(service.EnvFiles) != 1 || service.EnvFiles[0].Value != ".envrc" || service.EnvFiles[0].Source != SourceService {
		t.Fatalf("env files = %#v", service.EnvFiles)
	}
	if service.TracesExpected.Value != "true" || service.TracesExpected.Source != SourceService {
		t.Fatalf("traces expected = (%q, %q)", service.TracesExpected.Value, service.TracesExpected.Source)
	}
}
