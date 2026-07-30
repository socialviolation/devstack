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
	Env           WorkspaceManifestEnv            `yaml:"env,omitempty"`
	Groups        map[string][]string             `yaml:"groups,omitempty"`
	Dependencies  map[string][]string             `yaml:"dependencies,omitempty"`
	Calls         map[string][]string             `yaml:"calls,omitempty"`
	StartsAfter   map[string][]string             `yaml:"startsAfter,omitempty"`
	Environments  map[string]WorkspaceEnvironment `yaml:"environments,omitempty"`
	Hooks         []Hook                          `yaml:"hooks,omitempty"`
}

// ResourceDeps returns the effective startup ordering for svc: the deduped,
// sorted union of the legacy dependencies alias, startsAfter, and calls. A
// called service must be running before its caller, so calls fold into the
// ordering set that drives Tilt's resource_deps.
func (m *WorkspaceManifest) ResourceDeps(svc string) []string {
	return unionSorted(m.Dependencies[svc], m.StartsAfter[svc], m.Calls[svc])
}

// WorkspaceManifestEnv holds environment defaults that trickle down to every
// service in the workspace. A service's own env.values override these.
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
	// Enabled is the master switch for OTEL in this workspace: whether `up`
	// starts a local collector and whether OTEL export env is pushed down to
	// services. When nil (unset) it is inferred for backward compatibility from
	// local.enabled or the presence of a backend. Services are NOT assumed to be
	// instrumented unless this resolves true.
	Enabled *bool  `yaml:"enabled,omitempty"`
	Backend string `yaml:"backend,omitempty"`
	// Settings holds backend plugin configuration (upstream, protocol,
	// resource_attributes, ...). This file is committed, so credentials never
	// belong here — see SetObservabilitySettings.
	Settings map[string]string                      `yaml:"settings,omitempty"`
	Local    WorkspaceManifestObservabilityLocal    `yaml:"local,omitempty"`
	Defaults WorkspaceManifestObservabilityDefaults `yaml:"defaults,omitempty"`
}

// IsEnabled reports whether observability is turned on for the workspace.
// An explicit `enabled` wins; otherwise it is inferred from legacy config
// (local.enabled true, or a non-empty backend) so existing workspaces keep
// working without editing their manifest.
func (o WorkspaceManifestObservability) IsEnabled() bool {
	if o.Enabled != nil {
		return *o.Enabled
	}
	if o.Local.Enabled {
		return true
	}
	return strings.TrimSpace(o.Backend) != ""
}

// DefaultObservabilityBackend is the backend a workspace gets when its manifest
// names none: a single lightweight OpenObserve shared by the whole machine.
const DefaultObservabilityBackend = "openobserve"

// ResolvedBackend returns the backend plugin to use when observability is
// enabled: the explicit backend if set, otherwise the default. Returns "" when
// observability is disabled.
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
	Values map[string]string `yaml:"values,omitempty"`
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

// ServiceConfig points at the service's own repo files that already declare its
// config surface. Sources are read in order, later overriding earlier for a
// shared key. PortEnv names the env var that carries this service's listen port,
// the one the stack overlay overrides with the allocated port. Paths are relative
// to the service repo root.
type ServiceConfig struct {
	Sources []string `yaml:"sources,omitempty"`
	PortEnv string   `yaml:"portEnv,omitempty"`
}

// ServiceLink is a named URL surfaced in the dev daemon UI for a service.
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
	// TriggerMode is "manual" (start via devstack) or "auto" (start with the
	// daemon and re-run on change). Empty defaults to "manual".
	TriggerMode string `yaml:"triggerMode,omitempty"`
	// AutoStart starts the service as soon as the daemon comes up. Default false.
	AutoStart bool `yaml:"autoStart,omitempty"`
	// Watch lists file/directory paths that re-trigger the service on change.
	Watch []string `yaml:"watch,omitempty"`
}

type ServiceRun struct {
	Command string `yaml:"command,omitempty"`
}

// ServicePrep is a one-shot command run before the long-running service command
// (e.g. a build step).
type ServicePrep struct {
	Command string `yaml:"command,omitempty"`
	// FreePorts kills whatever still listens on this instance's own ports
	// before it starts, replacing the hand-written `fuser -k <port>/tcp` every
	// service used to carry. Empty means off; "all" (or the bool true) means
	// every port key in the manifest; otherwise it names port keys.
	//
	// An instance may only free the ports IT resolved to, never another
	// instance's — a stack owns its allocated ports from create to delete, and
	// base owns the ports it pins. That is what makes this safe where a copied
	// literal port was not: a stack's copy of a hardcoded base port would kill
	// base on every start.
	FreePorts FreePortsSpec `yaml:"freePorts,omitempty"`
}

// FreePortsSpec accepts `freePorts: true`, `freePorts: all`, or an explicit list
// of port keys, so the common case stays a single word.
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

// Enabled reports whether the service asked for any port freeing.
func (f FreePortsSpec) Enabled() bool { return f.All || len(f.Keys) > 0 }

// Resolve returns the ports to free for one instance, given the ports that
// instance actually resolved to. Unknown keys are an error rather than a
// silently skipped no-op: a typo would look like it worked.
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
			return nil, fmt.Errorf("service %q: runtime.prep.freePorts names port key %q, which it has no port for", service, k)
		}
		out = append(out, port)
	}
	return out, nil
}

type ServiceRestart struct {
	Strategy string `yaml:"strategy,omitempty"`
}

type ServiceHealthcheck struct {
	// Type is "http" or "exec". Empty means no healthcheck.
	Type string `yaml:"type,omitempty"`
	// URL is used by http checks (full URL). Alternatively Port+Path.
	URL  string `yaml:"url,omitempty"`
	Port int    `yaml:"port,omitempty"`
	Path string `yaml:"path,omitempty"`
	// Command is the shell command for exec checks (success = healthy).
	Command string `yaml:"command,omitempty"`
	// Probe tuning. Zero values fall back to generator defaults.
	PeriodSecs       int `yaml:"periodSecs,omitempty"`
	FailureThreshold int `yaml:"failureThreshold,omitempty"`
}

type ServiceEnv struct {
	// Values are inline KEY=VALUE pairs for this service (override workspace env).
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

// ObservabilityEnabled reports whether the workspace at workspacePath should run
// a local OTEL collector and push OTEL export env down to its services. Returns
// false when the workspace can't be resolved or has no observability configured.
func ObservabilityEnabled(workspacePath string) bool {
	rw, err := ResolveWorkspace(workspacePath)
	if err != nil || rw.Manifest == nil {
		return false
	}
	return rw.Manifest.Observability.IsEnabled()
}

// WorkspaceObservability returns the observability block of the workspace
// manifest at workspacePath, or the zero value when it can't be resolved.
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
	return validateHooks(m.Hooks, WorkspaceManifestFileName, true)
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
