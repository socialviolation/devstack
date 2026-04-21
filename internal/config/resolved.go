package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type ConfigSource string

const (
	SourceCLIFlag         ConfigSource = "cli-flag"
	SourceInvocationEnv   ConfigSource = "invocation-env"
	SourceRuntimeOverride ConfigSource = "runtime-override"
	SourceWorkspace       ConfigSource = "workspace-manifest"
	SourceService         ConfigSource = "service-manifest"
	SourceLegacy          ConfigSource = "legacy-config"
	SourceDefault         ConfigSource = "default"
	SourceCWD             ConfigSource = "cwd-detection"
)

type SourcedValue struct {
	Value  string
	Source ConfigSource
	Detail string
	Path   string
}

type ResolvedContext struct {
	WorkspaceRoot     SourcedValue
	WorkspaceName     SourcedValue
	EnvironmentName   SourcedValue
	CurrentService    SourcedValue
	WorkspaceManifest *WorkspaceManifest
	Workspace         *ResolvedWorkspace
}

type ResolvedServiceConfig struct {
	Name             string
	Path             SourcedValue
	RunCommand       SourcedValue
	WorkDir          SourcedValue
	HealthcheckType  SourcedValue
	HealthcheckURL   SourcedValue
	Groups           []SourcedValue
	Dependencies     []SourcedValue
	EnvFiles         []SourcedValue
	TelemetryService SourcedValue
	TracesExpected   SourcedValue
	LogsExpected     SourcedValue
	WorkspaceRoot    string
	WorkspaceName    string
	ManifestPath     string
}

type ResolveOptions struct {
	StartPath        string
	WorkspacePath    string
	EnvironmentName  string
	InvocationEnv    map[string]string
	RuntimeOverrides RuntimeOverrides
}

type RuntimeOverrides struct {
	WorkspacePath   string
	EnvironmentName string
	ServiceName     string
}

func ResolveContext(opts ResolveOptions) (*ResolvedContext, error) {
	startPath := opts.StartPath
	if startPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current directory: %w", err)
		}
		startPath = cwd
	}

	workspaceRoot, workspaceRootSource, detail, err := resolveWorkspaceRoot(opts, startPath)
	if err != nil {
		return nil, err
	}

	resolvedWorkspace, err := ResolveWorkspace(workspaceRoot)
	if err != nil {
		return nil, err
	}

	envValue, envSource, envDetail := resolveEnvironmentName(opts)
	serviceValue, serviceSource, serviceDetail := resolveCurrentService(resolvedWorkspace, opts, startPath)

	manifestPath := filepath.Join(workspaceRoot, WorkspaceManifestFileName)
	if resolvedWorkspace.Source == configFileName {
		manifestPath = filepath.Join(workspaceRoot, configFileName)
	}

	return &ResolvedContext{
		WorkspaceRoot: SourcedValue{
			Value:  workspaceRoot,
			Source: workspaceRootSource,
			Detail: detail,
			Path:   manifestPath,
		},
		WorkspaceName: SourcedValue{
			Value:  resolvedWorkspace.Manifest.Workspace.Name,
			Source: sourceForResolvedWorkspace(resolvedWorkspace),
			Detail: "workspace.name",
			Path:   manifestPath,
		},
		EnvironmentName: SourcedValue{
			Value:  envValue,
			Source: envSource,
			Detail: envDetail,
		},
		CurrentService: SourcedValue{
			Value:  serviceValue,
			Source: serviceSource,
			Detail: serviceDetail,
		},
		WorkspaceManifest: resolvedWorkspace.Manifest,
		Workspace:         resolvedWorkspace,
	}, nil
}

func ResolveServiceConfig(ctx *ResolvedContext, serviceName string) (*ResolvedServiceConfig, error) {
	if ctx == nil || ctx.Workspace == nil {
		return nil, fmt.Errorf("resolved context is required")
	}
	if serviceName == "" {
		serviceName = ctx.CurrentService.Value
	}
	service, ok := ctx.Workspace.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q not found in workspace %q", serviceName, ctx.WorkspaceName.Value)
	}

	groups := groupsForService(ctx.Workspace.Manifest.Groups, serviceName)
	deps := append([]string(nil), ctx.Workspace.Manifest.Dependencies[serviceName]...)
	sort.Strings(deps)

	manifestPath := filepath.Join(service.RepoPath, ServiceManifestFileName)
	pathSource := sourceForResolvedWorkspace(ctx.Workspace)
	if service.Source == ServiceManifestFileName {
		pathSource = SourceService
	}

	cfg := &ResolvedServiceConfig{
		Name: serviceName,
		Path: SourcedValue{
			Value:  service.RepoPath,
			Source: pathSource,
			Detail: "service path",
			Path:   manifestPath,
		},
		WorkspaceRoot: ctx.WorkspaceRoot.Value,
		WorkspaceName: ctx.WorkspaceName.Value,
		ManifestPath:  manifestPath,
	}

	if service.Manifest != nil {
		cfg.RunCommand = SourcedValue{Value: service.Manifest.Runtime.Run.Command, Source: SourceService, Detail: "runtime.run.command", Path: manifestPath}
		workDir := service.Manifest.Runtime.WorkDir
		if workDir == "" {
			workDir = "."
		}
		cfg.WorkDir = SourcedValue{Value: workDir, Source: defaultIf(service.Manifest.Runtime.WorkDir == "", SourceDefault, SourceService), Detail: "runtime.workDir", Path: manifestPath}
		cfg.HealthcheckType = SourcedValue{Value: service.Manifest.Runtime.Healthcheck.Type, Source: defaultIf(service.Manifest.Runtime.Healthcheck.Type == "", SourceDefault, SourceService), Detail: "runtime.healthcheck.type", Path: manifestPath}
		cfg.HealthcheckURL = SourcedValue{Value: service.Manifest.Runtime.Healthcheck.URL, Source: defaultIf(service.Manifest.Runtime.Healthcheck.URL == "", SourceDefault, SourceService), Detail: "runtime.healthcheck.url", Path: manifestPath}
		cfg.EnvFiles = sourcedList(service.Manifest.Env.Files, SourceService, "env.files", manifestPath)
		cfg.TelemetryService = SourcedValue{Value: service.Manifest.Telemetry.ServiceName, Source: defaultIf(service.Manifest.Telemetry.ServiceName == "", SourceDefault, SourceService), Detail: "telemetry.serviceName", Path: manifestPath}
		cfg.TracesExpected = SourcedValue{Value: boolString(service.Manifest.Telemetry.Traces.Expected), Source: defaultIf(!service.Manifest.Telemetry.Traces.Expected, SourceDefault, SourceService), Detail: "telemetry.traces.expected", Path: manifestPath}
		cfg.LogsExpected = SourcedValue{Value: boolString(service.Manifest.Telemetry.Logs.Expected), Source: defaultIf(!service.Manifest.Telemetry.Logs.Expected, SourceDefault, SourceService), Detail: "telemetry.logs.expected", Path: manifestPath}
	} else {
		cfg.RunCommand = SourcedValue{Source: SourceLegacy, Detail: "runtime.run.command unavailable in legacy config", Path: filepath.Join(ctx.WorkspaceRoot.Value, configFileName)}
		cfg.WorkDir = SourcedValue{Value: ".", Source: SourceDefault, Detail: "runtime.workDir"}
		cfg.HealthcheckType = SourcedValue{Source: SourceDefault, Detail: "runtime.healthcheck.type"}
		cfg.HealthcheckURL = SourcedValue{Source: SourceDefault, Detail: "runtime.healthcheck.url"}
		cfg.TelemetryService = SourcedValue{Source: SourceDefault, Detail: "telemetry.serviceName"}
		cfg.TracesExpected = SourcedValue{Value: "false", Source: SourceDefault, Detail: "telemetry.traces.expected"}
		cfg.LogsExpected = SourcedValue{Value: "false", Source: SourceDefault, Detail: "telemetry.logs.expected"}
	}

	cfg.Groups = sourcedList(groups, SourceWorkspace, "groups", filepath.Join(ctx.WorkspaceRoot.Value, manifestFileForWorkspace(ctx.Workspace)))
	cfg.Dependencies = sourcedList(deps, SourceWorkspace, "dependencies", filepath.Join(ctx.WorkspaceRoot.Value, manifestFileForWorkspace(ctx.Workspace)))
	return cfg, nil
}

func sourceForResolvedWorkspace(workspace *ResolvedWorkspace) ConfigSource {
	if workspace != nil && workspace.Source == WorkspaceManifestFileName {
		return SourceWorkspace
	}
	return SourceLegacy
}

func manifestFileForWorkspace(workspace *ResolvedWorkspace) string {
	if workspace != nil && workspace.Source == WorkspaceManifestFileName {
		return WorkspaceManifestFileName
	}
	return configFileName
}

func resolveWorkspaceRoot(opts ResolveOptions, startPath string) (string, ConfigSource, string, error) {
	if opts.WorkspacePath != "" {
		root, err := normalizeWorkspacePath(opts.WorkspacePath)
		if err != nil {
			return "", "", "", err
		}
		return root, SourceCLIFlag, "--workspace", nil
	}
	if envPath := opts.InvocationEnv["DEVSTACK_WORKSPACE"]; envPath != "" {
		root, err := normalizeWorkspacePath(envPath)
		if err != nil {
			return "", "", "", err
		}
		return root, SourceInvocationEnv, "DEVSTACK_WORKSPACE", nil
	}
	if opts.RuntimeOverrides.WorkspacePath != "" {
		root, err := normalizeWorkspacePath(opts.RuntimeOverrides.WorkspacePath)
		if err != nil {
			return "", "", "", err
		}
		return root, SourceRuntimeOverride, "runtime workspace override", nil
	}
	root, source, err := FindWorkspaceRoot(startPath)
	if err != nil {
		return "", "", "", err
	}
	return root, SourceCWD, source, nil
}

func resolveEnvironmentName(opts ResolveOptions) (string, ConfigSource, string) {
	if opts.EnvironmentName != "" {
		return opts.EnvironmentName, SourceCLIFlag, "--env"
	}
	if envName := opts.InvocationEnv["DEVSTACK_ENVIRONMENT"]; envName != "" {
		return envName, SourceInvocationEnv, "DEVSTACK_ENVIRONMENT"
	}
	if opts.RuntimeOverrides.EnvironmentName != "" {
		return opts.RuntimeOverrides.EnvironmentName, SourceRuntimeOverride, "runtime environment override"
	}
	return "local", SourceDefault, "default"
}

func resolveCurrentService(workspace *ResolvedWorkspace, opts ResolveOptions, startPath string) (string, ConfigSource, string) {
	if opts.RuntimeOverrides.ServiceName != "" {
		return opts.RuntimeOverrides.ServiceName, SourceRuntimeOverride, "runtime service override"
	}
	identity, err := ResolveIdentity(startPath)
	if err != nil {
		return "", SourceDefault, "not resolved"
	}
	if identity.ServiceName != "" {
		return identity.ServiceName, SourceCWD, "cwd service detection"
	}
	return "", SourceDefault, "not resolved"
}

func normalizeWorkspacePath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace path %s: %w", path, err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", err
	}
	if info, _ := os.Stat(absPath); info != nil && !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}
	if HasWorkspaceManifest(absPath) || hasLegacyConfig(absPath) {
		return filepath.Clean(absPath), nil
	}
	root, _, err := FindWorkspaceRoot(absPath)
	if err != nil {
		return "", err
	}
	return root, nil
}

func hasLegacyConfig(path string) bool {
	_, err := os.Stat(filepath.Join(path, configFileName))
	return err == nil
}

func groupsForService(groups map[string][]string, serviceName string) []string {
	var result []string
	for group, services := range groups {
		for _, service := range services {
			if service == serviceName {
				result = append(result, group)
				break
			}
		}
	}
	sort.Strings(result)
	return result
}

func sourcedList(values []string, source ConfigSource, detail, path string) []SourcedValue {
	result := make([]SourcedValue, 0, len(values))
	for _, value := range values {
		result = append(result, SourcedValue{Value: value, Source: source, Detail: detail, Path: path})
	}
	return result
}

func defaultIf(condition bool, whenTrue, whenFalse ConfigSource) ConfigSource {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
