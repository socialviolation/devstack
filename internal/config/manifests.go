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

const WorkspaceManifestVersion = 2

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
	Env           WorkspaceManifestEnv            `yaml:"env,omitempty"`
	Groups        map[string][]string             `yaml:"groups,omitempty"`
	Dependencies  map[string][]string             `yaml:"dependencies,omitempty"`
	Calls         map[string][]string             `yaml:"calls,omitempty"`
	StartsAfter   map[string][]string             `yaml:"startsAfter,omitempty"`
	Environments  map[string]WorkspaceEnvironment `yaml:"environments,omitempty"`
	Hooks         []Hook                          `yaml:"hooks,omitempty"`
}

func (m *WorkspaceManifest) ResourceDeps(svc string) []string {
	return unionSorted(m.Dependencies[svc], m.StartsAfter[svc], m.Calls[svc])
}

type WorkspaceManifestEnv struct {
	Values map[string]string `yaml:"values,omitempty"`
	Files  []string          `yaml:"files,omitempty"`
}

type WorkspaceManifestWorkspace struct {
	Name          string                         `yaml:"name"`
	Env           string                         `yaml:"env,omitempty"`
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
	Enabled *bool  `yaml:"enabled,omitempty"`
	Backend string `yaml:"backend,omitempty"`
	// Settings holds backend plugin configuration (upstream, protocol,
	// resource_attributes, ...). This file is committed, so credentials never
	// belong here — see SetObservabilitySettings.
	Settings map[string]string                      `yaml:"settings,omitempty"`
	Local    WorkspaceManifestObservabilityLocal    `yaml:"local,omitempty"`
	Defaults WorkspaceManifestObservabilityDefaults `yaml:"defaults,omitempty"`
}

func (o WorkspaceManifestObservability) IsEnabled() bool {
	if o.Enabled != nil {
		return *o.Enabled
	}
	if o.Local.Enabled {
		return true
	}
	return strings.TrimSpace(o.Backend) != ""
}

const DefaultObservabilityBackend = "openobserve"

func (o WorkspaceManifestObservability) ResolvedBackend() string {
	if !o.IsEnabled() {
		return ""
	}
	if b := strings.TrimSpace(o.Backend); b != "" {
		return b
	}
	return DefaultObservabilityBackend
}

type WorkspaceManifestObservabilityLocal struct {
	Enabled bool `yaml:"enabled,omitempty"`
}

type WorkspaceManifestObservabilityDefaults struct {
	RequireTraces bool `yaml:"requireTraces,omitempty"`
	RequireLogs   bool `yaml:"requireLogs,omitempty"`
}

// WorkspaceEnvironment is a named config-var patch. Manifests written by older
// devstack versions may still carry type/observability keys under an
// environment; they are ignored.
type WorkspaceEnvironment struct {
	Description string            `yaml:"description,omitempty"`
	Values      map[string]string `yaml:"values,omitempty"`
}

type ServiceManifest struct {
	Version   int                    `yaml:"version"`
	Service   ServiceManifestService `yaml:"service"`
	Runtime   ServiceRuntime         `yaml:"runtime,omitempty"`
	Ports     map[string]int         `yaml:"ports,omitempty"`
	Env       ServiceEnv             `yaml:"env,omitempty"`
	Config    ServiceConfig          `yaml:"config,omitempty"`
	Links     []ServiceLink          `yaml:"links,omitempty"`
	Telemetry ServiceTelemetry       `yaml:"telemetry,omitempty"`
	Hooks     []Hook                 `yaml:"hooks,omitempty"`
	Dev       map[string]any         `yaml:"dev,omitempty"`
}

type ServiceConfig struct {
	Sources []string `yaml:"sources,omitempty"`
	PortEnv string   `yaml:"portEnv,omitempty"`
}

type ServiceLink struct {
	URL   string `yaml:"url"`
	Label string `yaml:"label,omitempty"`
}

type ServiceManifestService struct {
	Name    string   `yaml:"name"`
	Env     string   `yaml:"env,omitempty"`
	Aliases []string `yaml:"aliases,omitempty"`
}

type ServiceRuntime struct {
	WorkDir     string             `yaml:"workDir,omitempty"`
	Run         ServiceRun         `yaml:"run,omitempty"`
	Prep        ServicePrep        `yaml:"prep,omitempty"`
	Restart     ServiceRestart     `yaml:"restart,omitempty"`
	Healthcheck ServiceHealthcheck `yaml:"healthcheck,omitempty"`
	TriggerMode string             `yaml:"triggerMode,omitempty"`
	AutoStart   bool               `yaml:"autoStart,omitempty"`
	Watch       []string           `yaml:"watch,omitempty"`
}

type ServiceRun struct {
	Command string `yaml:"command,omitempty"`
}

type ServicePrep struct {
	Command string `yaml:"command,omitempty"`
	// An instance may only free the ports IT resolved to, never another
	// instance's — a stack owns its allocated ports from create to delete, and
	// base owns the ports it pins. That is what makes this safe where a copied
	// literal port was not: a stack's copy of a hardcoded base port would kill
	// base on every start.
	FreePorts FreePortsSpec `yaml:"freePorts,omitempty"`
}

type FreePortsSpec struct {
	All  bool
	Keys []string
}

func (f *FreePortsSpec) UnmarshalYAML(value *yaml.Node) error {
	var b bool
	if value.Decode(&b) == nil {
		f.All = b
		return nil
	}
	var s string
	if value.Decode(&s) == nil {
		if s = strings.TrimSpace(s); s == "all" {
			f.All = true
			return nil
		} else if s != "" {
			f.Keys = []string{s}
			return nil
		}
		return nil
	}
	var keys []string
	if err := value.Decode(&keys); err != nil {
		return fmt.Errorf("runtime.prep.freePorts must be true, \"all\", or a list of port keys: %w", err)
	}
	f.Keys = keys
	return nil
}

func (f FreePortsSpec) MarshalYAML() (any, error) {
	if f.All {
		return true, nil
	}
	if len(f.Keys) == 0 {
		return nil, nil
	}
	return f.Keys, nil
}

func (f FreePortsSpec) Enabled() bool { return f.All || len(f.Keys) > 0 }

func (f FreePortsSpec) Resolve(service string, instancePorts map[string]int) ([]int, error) {
	if !f.Enabled() {
		return nil, nil
	}
	keys := f.Keys
	if f.All {
		keys = make([]string, 0, len(instancePorts))
		for k := range instancePorts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	out := make([]int, 0, len(keys))
	for _, k := range keys {
		port, ok := instancePorts[k]
		if !ok {
			return nil, fmt.Errorf("service %q: runtime.prep.freePorts names the port key %q, and the service has no port for that key", service, k)
		}
		out = append(out, port)
	}
	return out, nil
}

type ServiceRestart struct {
	Strategy string `yaml:"strategy,omitempty"`
}

type ServiceHealthcheck struct {
	Type             string `yaml:"type,omitempty"`
	URL              string `yaml:"url,omitempty"`
	Port             int    `yaml:"port,omitempty"`
	Path             string `yaml:"path,omitempty"`
	Command          string `yaml:"command,omitempty"`
	PeriodSecs       int    `yaml:"periodSecs,omitempty"`
	FailureThreshold int    `yaml:"failureThreshold,omitempty"`
}

type ServiceEnv struct {
	Values   map[string]string `yaml:"values,omitempty"`
	Files    []string          `yaml:"files,omitempty"`
	Required []string          `yaml:"required,omitempty"`
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
	Name         string
	RepoPath     string
	ManifestPath string
	Manifest     *ServiceManifest
	Source       string
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

const serviceManifestGlob = "devstack.*.yaml"

func IsServiceManifestName(base string) bool {
	if base == WorkspaceManifestFileName {
		return false
	}
	ok, _ := filepath.Match(serviceManifestGlob, base)
	return ok
}

func ServiceManifestFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !IsServiceManifestName(e.Name()) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

func HasWorkspaceManifest(workspacePath string) bool {
	_, err := os.Stat(WorkspaceManifestPath(workspacePath))
	return err == nil
}

func HasServiceManifest(repoPath string) bool {
	_, err := os.Stat(ServiceManifestPath(repoPath))
	return err == nil
}

func ObservabilityEnabled(workspacePath string) bool {
	rw, err := ResolveWorkspace(workspacePath)
	if err != nil || rw.Manifest == nil {
		return false
	}
	return rw.Manifest.Observability.IsEnabled()
}

func WorkspaceObservability(workspacePath string) WorkspaceManifestObservability {
	if !HasWorkspaceManifest(workspacePath) {
		return WorkspaceManifestObservability{}
	}
	manifest, err := LoadWorkspaceManifest(workspacePath)
	if err != nil {
		return WorkspaceManifestObservability{}
	}
	return manifest.Observability
}

func LoadWorkspaceManifest(workspacePath string) (*WorkspaceManifest, error) {
	path := WorkspaceManifestPath(workspacePath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("can not read the workspace manifest %s: %w", path, err)
	}

	var manifest WorkspaceManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("can not parse the workspace manifest %s: %w", path, err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("the workspace manifest %s is not valid: %w", path, err)
	}
	return &manifest, nil
}

func (m *WorkspaceManifest) Validate() error {
	if m == nil {
		return errors.New("the workspace manifest is nil")
	}
	if m.Version > WorkspaceManifestVersion {
		return fmt.Errorf("this workspace is at version %d, and this devstack knows version %d. A newer devstack wrote this manifest. To read it, install the current devstack: devstack upgrade", m.Version, WorkspaceManifestVersion)
	}
	if m.Version < 1 {
		return fmt.Errorf("devstack does not support version %d of the workspace manifest", m.Version)
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
	return validateHooks(m.Hooks, WorkspaceManifestFileName, true)
}

func LoadServiceManifest(repoPath string) (*ServiceManifest, error) {
	return LoadServiceManifestFile(ServiceManifestPath(repoPath))
}

func LoadServiceManifestFile(path string) (*ServiceManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("can not read the service manifest %s: %w", path, err)
	}

	var manifest ServiceManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("can not parse the service manifest %s: %w", path, err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("the service manifest %s is not valid: %w", path, err)
	}
	return &manifest, nil
}

func (m *ServiceManifest) Validate() error {
	if m == nil {
		return errors.New("the service manifest is nil")
	}
	if m.Version != 1 {
		return fmt.Errorf("devstack does not support version %d of the service manifest", m.Version)
	}
	if strings.TrimSpace(m.Service.Name) == "" {
		return errors.New("service.name is required")
	}
	if strings.TrimSpace(m.Runtime.Run.Command) == "" {
		return errors.New("runtime.run.command is required")
	}
	return validateHooks(m.Hooks, ServiceManifestFileName, false)
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
		files := ServiceManifestFiles(repoPath)
		if len(files) == 0 {
			// Keep the error the single-file loader gives, so a directory with no
			// manifest at all still says which file it looked for.
			_, err := LoadServiceManifest(repoPath)
			return err
		}
		for _, path := range files {
			serviceManifest, err := LoadServiceManifestFile(path)
			if err != nil {
				return err
			}
			name := serviceManifest.Service.Name
			if existing, ok := services[name]; ok {
				return fmt.Errorf("the service name %q is in %s and in %s. A service name must be unique", name, existing.ManifestPath, path)
			}
			services[name] = ResolvedService{
				Name:         name,
				RepoPath:     repoPath,
				ManifestPath: path,
				Manifest:     serviceManifest,
				Source:       ServiceManifestFileName,
			}
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
			// register takes a directory and reads every manifest in it, so a
			// directory holding several must be registered once and not once for
			// each file — the second pass would report its own services as
			// duplicates of themselves.
			seen := map[string]bool{}
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
				if !IsServiceManifestName(d.Name()) {
					return nil
				}
				dir := filepath.Dir(path)
				if seen[dir] {
					return nil
				}
				seen[dir] = true
				return register(dir)
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
		Version: WorkspaceManifestVersion,
		Workspace: WorkspaceManifestWorkspace{
			Name: filepath.Base(workspacePath),
			RepoDiscovery: WorkspaceManifestRepoDiscovery{
				Mode: RepoDiscoveryModeExplicit,
			},
		},
		Groups:       cloneStringSlicesMap(cfg.Groups),
		Dependencies: cloneStringSlicesMap(cfg.Deps),
	}

	paths := make([]string, 0, len(cfg.ServicePaths))
	for _, servicePath := range cfg.ServicePaths {
		rel, err := filepath.Rel(workspacePath, servicePath)
		if err != nil {
			return nil, fmt.Errorf("can not make the service path %s relative: %w", servicePath, err)
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
		return nil, fmt.Errorf("can not resolve the path %s: %w", path, err)
	}
	absPath = filepath.Clean(absPath)

	identity := &ResolvedIdentity{
		WorkspaceRoot: workspaceRoot,
		WorkspaceName: resolved.Manifest.Workspace.Name,
		Source:        source,
	}
	best := -1
	ambiguous := false
	for name, service := range resolved.Services {
		if absPath != service.RepoPath && !strings.HasPrefix(absPath, service.RepoPath+string(filepath.Separator)) {
			continue
		}
		switch n := len(service.RepoPath); {
		case n > best:
			best, identity.ServiceName, ambiguous = n, name, false
		case n == best:
			ambiguous = true
		}
	}
	if ambiguous {
		identity.ServiceName = ""
	}
	return identity, nil
}

func FindWorkspaceRoot(path string) (string, string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("can not resolve the path %s: %w", path, err)
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
	return "", "", fmt.Errorf("devstack can not find a workspace manifest or a %s above %s", configFileName, path)
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
		return nil, fmt.Errorf("can not read the devstack config: %w", err)
	}

	var cfg WorkspaceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("can not parse the devstack config: %w", err)
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

func unionSorted(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, v := range list {
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
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
