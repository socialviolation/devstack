package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	WorkspaceManifestFileName = "devstack.workspace.yaml"
	ServiceManifestFileName   = "devstack.service.yaml"
)

type RepoDiscoveryMode string

const (
	RepoDiscoveryModeExplicit RepoDiscoveryMode = "explicit"
	RepoDiscoveryModeScan     RepoDiscoveryMode = "scan"
)

type WorkspaceManifest struct {
	Version       int                             `yaml:"version"`
	Workspace     WorkspaceManifestWorkspace      `yaml:"workspace"`
	Runtime       WorkspaceManifestRuntime        `yaml:"runtime,omitempty"`
	Observability WorkspaceManifestObservability  `yaml:"observability,omitempty"`
	Groups        map[string][]string             `yaml:"groups,omitempty"`
	Dependencies  map[string][]string             `yaml:"dependencies,omitempty"`
	Environments  map[string]WorkspaceEnvironment `yaml:"environments,omitempty"`
}

type WorkspaceManifestWorkspace struct {
	Name          string                         `yaml:"name"`
	RepoDiscovery WorkspaceManifestRepoDiscovery `yaml:"repoDiscovery,omitempty"`
}

type WorkspaceManifestRepoDiscovery struct {
	Mode  RepoDiscoveryMode `yaml:"mode,omitempty"`
	Repos []string          `yaml:"repos,omitempty"`
	Roots []string          `yaml:"roots,omitempty"`
}

type WorkspaceManifestRuntime struct {
	Orchestrator string                 `yaml:"orchestrator,omitempty"`
	Infra        WorkspaceManifestInfra `yaml:"infra,omitempty"`
}

type WorkspaceManifestInfra struct {
	Provider     string   `yaml:"provider,omitempty"`
	ComposeFiles []string `yaml:"composeFiles,omitempty"`
}

type WorkspaceManifestObservability struct {
	Backend  string                                 `yaml:"backend,omitempty"`
	Local    WorkspaceManifestObservabilityLocal    `yaml:"local,omitempty"`
	Defaults WorkspaceManifestObservabilityDefaults `yaml:"defaults,omitempty"`
}

type WorkspaceManifestObservabilityLocal struct {
	Enabled bool `yaml:"enabled,omitempty"`
}

type WorkspaceManifestObservabilityDefaults struct {
	RequireTraces bool `yaml:"requireTraces,omitempty"`
	RequireLogs   bool `yaml:"requireLogs,omitempty"`
}

type WorkspaceEnvironment struct {
	Type          string                            `yaml:"type,omitempty"`
	Observability WorkspaceEnvironmentObservability `yaml:"observability,omitempty"`
}

type WorkspaceEnvironmentObservability struct {
	Backend      string `yaml:"backend,omitempty"`
	URL          string `yaml:"url,omitempty"`
	OTLPEndpoint string `yaml:"otlpEndpoint,omitempty"`
}

type ServiceManifest struct {
	Version   int                    `yaml:"version"`
	Service   ServiceManifestService `yaml:"service"`
	Runtime   ServiceRuntime         `yaml:"runtime,omitempty"`
	Ports     map[string]int         `yaml:"ports,omitempty"`
	Env       ServiceEnv             `yaml:"env,omitempty"`
	Telemetry ServiceTelemetry       `yaml:"telemetry,omitempty"`
	Dev       map[string]any         `yaml:"dev,omitempty"`
}

type ServiceManifestService struct {
	Name    string   `yaml:"name"`
	Aliases []string `yaml:"aliases,omitempty"`
}

type ServiceRuntime struct {
	WorkDir     string             `yaml:"workDir,omitempty"`
	Run         ServiceRun         `yaml:"run,omitempty"`
	Restart     ServiceRestart     `yaml:"restart,omitempty"`
	Healthcheck ServiceHealthcheck `yaml:"healthcheck,omitempty"`
}

type ServiceRun struct {
	Command string `yaml:"command,omitempty"`
}

type ServiceRestart struct {
	Strategy string `yaml:"strategy,omitempty"`
}

type ServiceHealthcheck struct {
	Type string `yaml:"type,omitempty"`
	URL  string `yaml:"url,omitempty"`
}

type ServiceEnv struct {
	Files    []string `yaml:"files,omitempty"`
	Required []string `yaml:"required,omitempty"`
}

type ServiceTelemetry struct {
	Traces      ServiceTelemetrySignal `yaml:"traces,omitempty"`
	Logs        ServiceTelemetrySignal `yaml:"logs,omitempty"`
	Metrics     ServiceTelemetrySignal `yaml:"metrics,omitempty"`
	ServiceName string                 `yaml:"serviceName,omitempty"`
}

type ServiceTelemetrySignal struct {
	Expected bool `yaml:"expected,omitempty"`
}

type ResolvedWorkspace struct {
	RootPath string
	Source   string
	Manifest *WorkspaceManifest
	Services map[string]ResolvedService
}

type ResolvedService struct {
	Name     string
	RepoPath string
	Manifest *ServiceManifest
	Source   string
}

type ResolvedIdentity struct {
	WorkspaceRoot string
	WorkspaceName string
	ServiceName   string
	Source        string
}

func WorkspaceManifestPath(workspacePath string) string {
	return filepath.Join(workspacePath, WorkspaceManifestFileName)
}

func ServiceManifestPath(repoPath string) string {
	return filepath.Join(repoPath, ServiceManifestFileName)
}

func HasWorkspaceManifest(workspacePath string) bool {
	_, err := os.Stat(WorkspaceManifestPath(workspacePath))
	return err == nil
}

func HasServiceManifest(repoPath string) bool {
	_, err := os.Stat(ServiceManifestPath(repoPath))
	return err == nil
}

func LoadWorkspaceManifest(workspacePath string) (*WorkspaceManifest, error) {
	path := WorkspaceManifestPath(workspacePath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read workspace manifest %s: %w", path, err)
	}

	var manifest WorkspaceManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse workspace manifest %s: %w", path, err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid workspace manifest %s: %w", path, err)
	}
	return &manifest, nil
}

func (m *WorkspaceManifest) Validate() error {
	if m == nil {
		return errors.New("workspace manifest is nil")
	}
	if m.Version != 1 {
		return fmt.Errorf("unsupported workspace manifest version %d", m.Version)
	}
	if strings.TrimSpace(m.Workspace.Name) == "" {
		return errors.New("workspace.name is required")
	}
	mode := m.Workspace.RepoDiscovery.Mode
	if mode == "" {
		mode = RepoDiscoveryModeExplicit
	}
	if mode != RepoDiscoveryModeExplicit && mode != RepoDiscoveryModeScan {
		return fmt.Errorf("workspace.repoDiscovery.mode must be %q or %q", RepoDiscoveryModeExplicit, RepoDiscoveryModeScan)
	}
	if mode == RepoDiscoveryModeExplicit && len(m.Workspace.RepoDiscovery.Repos) == 0 {
		return errors.New("workspace.repoDiscovery.repos is required for explicit mode")
	}
	if mode == RepoDiscoveryModeScan && len(m.Workspace.RepoDiscovery.Roots) == 0 {
		return errors.New("workspace.repoDiscovery.roots is required for scan mode")
	}
	return nil
}

func LoadServiceManifest(repoPath string) (*ServiceManifest, error) {
	path := ServiceManifestPath(repoPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read service manifest %s: %w", path, err)
	}

	var manifest ServiceManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse service manifest %s: %w", path, err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid service manifest %s: %w", path, err)
	}
	return &manifest, nil
}

func (m *ServiceManifest) Validate() error {
	if m == nil {
		return errors.New("service manifest is nil")
	}
	if m.Version != 1 {
		return fmt.Errorf("unsupported service manifest version %d", m.Version)
	}
	if strings.TrimSpace(m.Service.Name) == "" {
		return errors.New("service.name is required")
	}
	if strings.TrimSpace(m.Runtime.Run.Command) == "" {
		return errors.New("runtime.run.command is required")
	}
	return nil
}

func ResolveWorkspace(workspacePath string) (*ResolvedWorkspace, error) {
	workspacePath = filepath.Clean(workspacePath)
	if HasWorkspaceManifest(workspacePath) {
		manifest, err := LoadWorkspaceManifest(workspacePath)
		if err != nil {
			return nil, err
		}
		services, err := resolveManifestServices(workspacePath, manifest)
		if err != nil {
			return nil, err
		}
		return &ResolvedWorkspace{
			RootPath: workspacePath,
			Source:   WorkspaceManifestFileName,
			Manifest: manifest,
			Services: services,
		}, nil
	}

	legacy, err := loadLegacyConfig(workspacePath)
	if err != nil {
		return nil, err
	}
	manifest, err := LegacyWorkspaceManifest(workspacePath, legacy)
	if err != nil {
		return nil, err
	}
	services := make(map[string]ResolvedService, len(legacy.ServicePaths))
	for name, repoPath := range legacy.ServicePaths {
		services[name] = ResolvedService{
			Name:     name,
			RepoPath: filepath.Clean(repoPath),
			Source:   configFileName,
		}
	}
	return &ResolvedWorkspace{
		RootPath: workspacePath,
		Source:   configFileName,
		Manifest: manifest,
		Services: services,
	}, nil
}

func resolveManifestServices(workspacePath string, manifest *WorkspaceManifest) (map[string]ResolvedService, error) {
	services := map[string]ResolvedService{}
	mode := manifest.Workspace.RepoDiscovery.Mode
	if mode == "" {
		mode = RepoDiscoveryModeExplicit
	}

	register := func(repoPath string) error {
		repoPath = filepath.Clean(repoPath)
		serviceManifest, err := LoadServiceManifest(repoPath)
		if err != nil {
			return err
		}
		name := serviceManifest.Service.Name
		if existing, ok := services[name]; ok {
			return fmt.Errorf("duplicate service name %q in %s and %s", name, existing.RepoPath, repoPath)
		}
		services[name] = ResolvedService{
			Name:     name,
			RepoPath: repoPath,
			Manifest: serviceManifest,
			Source:   ServiceManifestFileName,
		}
		return nil
	}

	switch mode {
	case RepoDiscoveryModeExplicit:
		for _, repo := range manifest.Workspace.RepoDiscovery.Repos {
			if strings.TrimSpace(repo) == "" {
				continue
			}
			if err := register(resolveRelative(workspacePath, repo)); err != nil {
				return nil, err
			}
		}
	case RepoDiscoveryModeScan:
		for _, root := range manifest.Workspace.RepoDiscovery.Roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			absRoot := resolveRelative(workspacePath, root)
			err := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					if d.Name() == ".git" {
						return filepath.SkipDir
					}
					return nil
				}
				if d.Name() != ServiceManifestFileName {
					return nil
				}
				return register(filepath.Dir(path))
			})
			if err != nil {
				return nil, err
			}
		}
	}

	return services, nil
}

func (rw *ResolvedWorkspace) ToLegacyConfig() *WorkspaceConfig {
	cfg := &WorkspaceConfig{
		Deps:         cloneStringSlicesMap(rw.Manifest.Dependencies),
		Groups:       cloneStringSlicesMap(rw.Manifest.Groups),
		ServicePaths: map[string]string{},
		OtelPlugin:   rw.Manifest.Observability.Backend,
	}
	for name, service := range rw.Services {
		cfg.ServicePaths[name] = service.RepoPath
	}
	if cfg.Deps == nil {
		cfg.Deps = map[string][]string{}
	}
	if cfg.Groups == nil {
		cfg.Groups = map[string][]string{}
	}
	return cfg
}

func LegacyWorkspaceManifest(workspacePath string, cfg *WorkspaceConfig) (*WorkspaceManifest, error) {
	workspacePath = filepath.Clean(workspacePath)
	manifest := &WorkspaceManifest{
		Version: 1,
		Workspace: WorkspaceManifestWorkspace{
			Name: filepath.Base(workspacePath),
			RepoDiscovery: WorkspaceManifestRepoDiscovery{
				Mode: RepoDiscoveryModeExplicit,
			},
		},
		Groups:       cloneStringSlicesMap(cfg.Groups),
		Dependencies: cloneStringSlicesMap(cfg.Deps),
	}

	if cfg.OtelPlugin != "" {
		manifest.Observability.Backend = cfg.OtelPlugin
	}

	paths := make([]string, 0, len(cfg.ServicePaths))
	for _, servicePath := range cfg.ServicePaths {
		rel, err := filepath.Rel(workspacePath, servicePath)
		if err != nil {
			return nil, fmt.Errorf("failed to relativize service path %s: %w", servicePath, err)
		}
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	manifest.Workspace.RepoDiscovery.Repos = paths
	return manifest, nil
}

func ResolveIdentity(path string) (*ResolvedIdentity, error) {
	workspaceRoot, source, err := FindWorkspaceRoot(path)
	if err != nil {
		return nil, err
	}
	resolved, err := ResolveWorkspace(workspaceRoot)
	if err != nil {
		return nil, err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path %s: %w", path, err)
	}
	absPath = filepath.Clean(absPath)

	identity := &ResolvedIdentity{
		WorkspaceRoot: workspaceRoot,
		WorkspaceName: resolved.Manifest.Workspace.Name,
		Source:        source,
	}
	for name, service := range resolved.Services {
		if absPath == service.RepoPath || strings.HasPrefix(absPath, service.RepoPath+string(filepath.Separator)) {
			identity.ServiceName = name
			break
		}
	}
	return identity, nil
}

func FindWorkspaceRoot(path string) (string, string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve path %s: %w", path, err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}

	for current := absPath; ; current = filepath.Dir(current) {
		if HasWorkspaceManifest(current) {
			return current, WorkspaceManifestFileName, nil
		}
		if _, err := os.Stat(filepath.Join(current, configFileName)); err == nil {
			return current, configFileName, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return "", "", fmt.Errorf("no workspace manifest or %s found above %s", configFileName, path)
}

func loadLegacyConfig(workspacePath string) (*WorkspaceConfig, error) {
	path := filepath.Join(workspacePath, configFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &WorkspaceConfig{
				Deps:         map[string][]string{},
				Groups:       map[string][]string{},
				ServicePaths: map[string]string{},
			}, nil
		}
		return nil, fmt.Errorf("failed to read devstack config: %w", err)
	}

	var cfg WorkspaceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse devstack config: %w", err)
	}
	if cfg.Deps == nil {
		cfg.Deps = map[string][]string{}
	}
	if cfg.Groups == nil {
		cfg.Groups = map[string][]string{}
	}
	if cfg.ServicePaths == nil {
		cfg.ServicePaths = map[string]string{}
	}
	for name, servicePath := range cfg.ServicePaths {
		cfg.ServicePaths[name] = filepath.Clean(servicePath)
	}
	return &cfg, nil
}

func resolveRelative(basePath, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(basePath, value))
}

func cloneStringSlicesMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}
