package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/socialviolation/devstack/internal/config"
)

// EnvironmentType describes whether an environment is locally Tilt-managed or remote-only.
type EnvironmentType string

const (
	// EnvironmentTypeLocal is a locally managed environment with Tilt + embedded SigNoz.
	// All MCP tools are available including restart, stop, and configure.
	EnvironmentTypeLocal EnvironmentType = "local"

	// EnvironmentTypeRemote is a remote-only environment (staging, prod, etc).
	// Only observability tools are available — no service control.
	EnvironmentTypeRemote EnvironmentType = "remote"
)

// ObservabilityConfig holds the connection config for an observability backend.
type ObservabilityConfig struct {
	Backend      string `json:"backend"`                 // "signoz" (default and only supported value)
	URL          string `json:"url"`                     // Base URL, e.g. "http://localhost:3301"
	OTLPEndpoint string `json:"otlp_endpoint,omitempty"` // OTLP ingestion URL for collector (e.g. https://otel.company.com:4318)
	APIKey       string `json:"api_key,omitempty"`       // Optional API key for remote instances
}

// Environment represents a named deployment target with associated observability config.
// Local environments also have Tilt for service control. Remote environments are read-only.
type Environment struct {
	Type          EnvironmentType     `json:"type"`
	Observability ObservabilityConfig `json:"observability"`
}

// Workspace represents a registered development workspace.
type Workspace struct {
	Name     string `json:"name"`
	Path     string `json:"path"`      // absolute path to workspace root (e.g. /home/nick/dev/navexa)
	TiltPort int    `json:"tilt_port"` // port Tilt API listens on for this workspace

	// SigNoz port overrides. Zero means use the default.
	OtelUIPort       int `json:"otel_ui_port,omitempty"`        // SigNoz UI + query API (default 3301)
	OtelOTLPGRPCPort int `json:"otel_otlp_grpc_port,omitempty"` // OTLP gRPC (default 4317)
	OtelOTLPHTTPPort int `json:"otel_otlp_http_port,omitempty"` // OTLP HTTP (default 4318)

	// OtelPlugin names the active OTEL plugin (default "signoz").
	OtelPlugin string `json:"otel_plugin,omitempty"`
	// OtelPluginConfig holds plugin-specific configuration key-value pairs.
	OtelPluginConfig map[string]string `json:"otel_plugin_config,omitempty"`

	// Tunnel remote defaults for `devstack tunnel push/pull`. Machine-specific, so
	// they live in the per-user registry rather than the committed project config.
	TunnelHost string `json:"tunnel_host,omitempty"`
	TunnelUser string `json:"tunnel_user,omitempty"`

	// Active reports whether this workspace's services are folded into the one
	// host Tilt daemon. `devstack workspace up` sets it, `down` clears it.
	Active bool `json:"active,omitempty"`
}

// OverlayProjectConfig reads the workspace's .devstack.json and overlays any OTEL
// plugin config found there, letting per-project config take precedence over the registry.
func (ws *Workspace) OverlayProjectConfig() {
	if ws.Path == "" {
		return
	}
	cfg, err := config.Load(ws.Path)
	if err != nil || cfg == nil {
		return
	}
	if cfg.OtelPlugin != "" {
		ws.OtelPlugin = cfg.OtelPlugin
	}
	if len(cfg.OtelPluginConfig) > 0 {
		merged := make(map[string]string, len(ws.OtelPluginConfig)+len(cfg.OtelPluginConfig))
		for k, v := range ws.OtelPluginConfig {
			merged[k] = v
		}
		for k, v := range cfg.OtelPluginConfig {
			merged[k] = v
		}
		ws.OtelPluginConfig = merged
	}
}

// PluginConfig returns the value for a plugin config key, or "" if not set.
func (ws *Workspace) PluginConfig(key string) string {
	if ws.OtelPluginConfig == nil {
		return ""
	}
	return ws.OtelPluginConfig[key]
}

// RegistryPath returns the path to the workspace registry JSON file.
// Expands ~ via os.UserHomeDir.
func RegistryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback: use HOME env var
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "devstack", "workspaces.json")
}

// DataRoot returns the directory holding every workspace's runtime data.
func DataRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".local", "share", "devstack")
}

// DataDir returns the runtime data directory for a named workspace.
func DataDir(name string) string {
	return filepath.Join(DataRoot(), name) + "/"
}

// PIDFile returns the path to the Tilt PID file for a named workspace.
func PIDFile(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".local", "share", "devstack", name, "tilt.pid")
}

// LogFile returns the path to the Tilt log file for a named workspace.
func LogFile(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".local", "share", "devstack", name, "tilt.log")
}

// hostKey is the reserved runtime key the single host Tilt daemon files its
// data, PID, log, session, and Tiltfile under. It is not a registry workspace,
// so it never collides with a real workspace name.
const hostKey = "_devstack-host"

// HostTiltPort is the fixed API port the one host Tilt daemon listens on. It is
// distinct from the per-workspace TiltPort range (10350+).
const HostTiltPort = 10300

// HostPIDFile returns the PID file path for the host Tilt daemon.
func HostPIDFile() string { return PIDFile(hostKey) }

// HostLogFile returns the log file path for the host Tilt daemon.
func HostLogFile() string { return LogFile(hostKey) }

// HostTiltDir returns the host Tilt daemon's working directory, where its
// generated Tiltfile lives.
func HostTiltDir() string { return DataDir(hostKey) }

// HostWorkspace returns the synthetic workspace the host daemon's session state
// is keyed to (its runtime key and fixed port). It is never registered.
func HostWorkspace() *Workspace {
	return &Workspace{Name: hostKey, TiltPort: HostTiltPort}
}

// Load reads and parses the registry JSON file.
// Returns an empty slice (not an error) if the file doesn't exist.
func Load() ([]Workspace, error) {
	path := RegistryPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Workspace{}, nil
		}
		return nil, fmt.Errorf("failed to read registry: %w", err)
	}

	var workspaces []Workspace
	if err := json.Unmarshal(data, &workspaces); err != nil {
		return nil, fmt.Errorf("failed to parse registry: %w", err)
	}
	return workspaces, nil
}

// Save writes the registry JSON with indentation, creating parent dirs if needed.
func Save(workspaces []Workspace) error {
	path := RegistryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create registry directory: %w", err)
	}

	data, err := json.MarshalIndent(workspaces, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal registry: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write registry: %w", err)
	}
	return nil
}

// expandPath expands a leading ~ in a path using os.UserHomeDir.
func expandPath(path string) string {
	if !strings.HasPrefix(path, "~/") && path != "~" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return home + path[1:]
}

// Register adds a workspace, or updates the existing entry at the same path.
// A name that collides case-insensitively with an entry at a different path is
// rejected. If TiltPort is 0, a port is auto-assigned starting from 10350.
func Register(ws Workspace) error {
	ws.Path = filepath.Clean(expandPath(ws.Path))

	return withRegistryLock(func() error {
		workspaces, err := Load()
		if err != nil {
			return err
		}

		if ws.TiltPort == 0 {
			port, err := nextPortFrom(workspaces)
			if err != nil {
				return err
			}
			ws.TiltPort = port
		}

		lowerName := strings.ToLower(ws.Name)
		for _, existing := range workspaces {
			if existing.Path != ws.Path && strings.ToLower(existing.Name) == lowerName {
				return fmt.Errorf("workspace name %q already registered at %s", ws.Name, existing.Path)
			}
		}

		for i, existing := range workspaces {
			if existing.Path == ws.Path {
				workspaces[i] = ws
				return Save(workspaces)
			}
		}

		workspaces = append(workspaces, ws)
		return Save(workspaces)
	})
}

// withRegistryLock runs fn while holding an exclusive advisory lock on a
// dedicated lockfile beside the registry, serialising concurrent read-compute-write
// sequences (allocate a port, then Save) across processes and goroutines. The
// registry file itself is not locked because Save rewrites it.
func withRegistryLock(fn func() error) error {
	lockPath := RegistryPath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return fmt.Errorf("failed to create registry directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to open registry lock: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("failed to lock registry: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// All returns all registered workspaces.
func All() ([]Workspace, error) {
	return Load()
}

// FindByName returns the workspace with the given name (case-insensitive).
func FindByName(name string) (*Workspace, error) {
	workspaces, err := Load()
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(name)
	for _, ws := range workspaces {
		if strings.ToLower(ws.Name) == lower {
			w := ws
			return &w, nil
		}
	}
	return nil, fmt.Errorf("workspace %q not found", name)
}

// FindByPath returns the workspace matching the given path exactly (after cleaning).
func FindByPath(path string) (*Workspace, error) {
	clean := filepath.Clean(path)
	workspaces, err := Load()
	if err != nil {
		return nil, err
	}
	for _, ws := range workspaces {
		if filepath.Clean(ws.Path) == clean {
			w := ws
			return &w, nil
		}
	}
	return nil, fmt.Errorf("no workspace registered at path %q", path)
}

// DetectFromCwd returns the registered workspace whose path is the longest prefix
// of the current working directory, so a nested worktree resolves to itself rather
// than to an ancestor workspace. Returns an error if the cwd is not inside any
// registered workspace.
func DetectFromCwd() (*Workspace, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	workspaces, err := Load()
	if err != nil {
		return nil, err
	}

	best := -1
	bestLen := -1
	for i := range workspaces {
		wsPath := workspaces[i].Path
		if resolved, err := filepath.EvalSymlinks(wsPath); err == nil {
			wsPath = resolved
		}
		if cwd == wsPath || strings.HasPrefix(cwd, wsPath+"/") {
			if len(wsPath) > bestLen {
				bestLen = len(wsPath)
				best = i
			}
		}
	}
	if best >= 0 {
		w := workspaces[best]
		return &w, nil
	}
	return nil, fmt.Errorf("not inside a registered devstack workspace. Run: devstack register")
}

const defaultOtelUIPort = 3301
const defaultOtelOTLPGRPCPort = 4317
const defaultOtelOTLPHTTPPort = 4318

// UIPort returns the effective SigNoz UI/query port for a managed workspace.
func (ws *Workspace) UIPort() int {
	if ws.OtelUIPort > 0 {
		return ws.OtelUIPort
	}
	return defaultOtelUIPort
}

// GRPCPort returns the effective OTLP gRPC port for a managed workspace.
func (ws *Workspace) GRPCPort() int {
	if ws.OtelOTLPGRPCPort > 0 {
		return ws.OtelOTLPGRPCPort
	}
	return defaultOtelOTLPGRPCPort
}

// HTTPPort returns the effective OTLP HTTP port for a managed workspace.
func (ws *Workspace) HTTPPort() int {
	if ws.OtelOTLPHTTPPort > 0 {
		return ws.OtelOTLPHTTPPort
	}
	return defaultOtelOTLPHTTPPort
}

// OtelOTLPEndpoint returns the OTLP gRPC endpoint services should push to.
func OtelOTLPEndpoint(ws *Workspace) string {
	return fmt.Sprintf("http://localhost:%d", ws.GRPCPort())
}

// OtelQueryEndpoint returns the SigNoz query API base URL used by MCP tools.
func OtelQueryEndpoint(ws *Workspace) string {
	return fmt.Sprintf("http://localhost:%d", ws.UIPort())
}

// UpdateOtelPlugin sets the OTEL plugin name and config for a workspace.
func UpdateOtelPlugin(name, pluginName string, config map[string]string) error {
	workspaces, err := Load()
	if err != nil {
		return err
	}
	lower := strings.ToLower(name)
	for i, ws := range workspaces {
		if strings.ToLower(ws.Name) == lower {
			workspaces[i].OtelPlugin = pluginName
			if config != nil {
				workspaces[i].OtelPluginConfig = config
			}
			return Save(workspaces)
		}
	}
	return fmt.Errorf("workspace %q not found", name)
}

// UpdateOtelPorts sets port overrides for a workspace.
// Pass 0 for any port to leave it unchanged.
func UpdateOtelPorts(name string, uiPort, grpcPort, httpPort int) error {
	workspaces, err := Load()
	if err != nil {
		return err
	}
	for i, ws := range workspaces {
		if strings.ToLower(ws.Name) == strings.ToLower(name) {
			if uiPort > 0 {
				workspaces[i].OtelUIPort = uiPort
			}
			if grpcPort > 0 {
				workspaces[i].OtelOTLPGRPCPort = grpcPort
			}
			if httpPort > 0 {
				workspaces[i].OtelOTLPHTTPPort = httpPort
			}
			return Save(workspaces)
		}
	}
	return fmt.Errorf("workspace %q not found", name)
}

// UpdateTunnelRemote persists the default tunnel host/user for a named workspace.
// Empty values are left unchanged so a --user override doesn't wipe a saved host.
func UpdateTunnelRemote(name, host, user string) error {
	workspaces, err := Load()
	if err != nil {
		return err
	}
	for i, ws := range workspaces {
		if strings.ToLower(ws.Name) == strings.ToLower(name) {
			if host != "" {
				workspaces[i].TunnelHost = host
			}
			if user != "" {
				workspaces[i].TunnelUser = user
			}
			return Save(workspaces)
		}
	}
	return fmt.Errorf("workspace %q not found", name)
}

// UpdatePort updates the TiltPort for a named workspace in the registry.
func UpdatePort(name string, port int) error {
	workspaces, err := Load()
	if err != nil {
		return err
	}
	for i, ws := range workspaces {
		if strings.ToLower(ws.Name) == strings.ToLower(name) {
			workspaces[i].TiltPort = port
			return Save(workspaces)
		}
	}
	return fmt.Errorf("workspace %q not found", name)
}

// SetWorkspaceActive marks a workspace active or inactive and persists it. An
// active workspace's services are folded into the one host Tilt daemon. Errors
// if the workspace is unknown.
func SetWorkspaceActive(name string, active bool) error {
	return withRegistryLock(func() error {
		workspaces, err := Load()
		if err != nil {
			return err
		}
		lower := strings.ToLower(name)
		for i := range workspaces {
			if strings.ToLower(workspaces[i].Name) == lower {
				workspaces[i].Active = active
				return Save(workspaces)
			}
		}
		return fmt.Errorf("workspace %q not found", name)
	})
}

// ActiveWorkspaces returns the registered workspaces marked active, in registry order.
func ActiveWorkspaces() ([]Workspace, error) {
	workspaces, err := Load()
	if err != nil {
		return nil, err
	}
	var active []Workspace
	for _, ws := range workspaces {
		if ws.Active {
			active = append(active, ws)
		}
	}
	return active, nil
}

// AnyWorkspaceActive reports whether any registered workspace is active.
func AnyWorkspaceActive() (bool, error) {
	workspaces, err := Load()
	if err != nil {
		return false, err
	}
	for _, ws := range workspaces {
		if ws.Active {
			return true, nil
		}
	}
	return false, nil
}

// ResolveEnvironment returns the named environment config.
func (ws *Workspace) ResolveEnvironment(name string) (Environment, bool) {
	if name == "local" || name == "" {
		return Environment{
			Type: EnvironmentTypeLocal,
			Observability: ObservabilityConfig{
				Backend: "signoz",
				URL:     fmt.Sprintf("http://localhost:%d", ws.UIPort()),
			},
		}, true
	}
	return Environment{}, false
}

const minPort = 10350
const portScanLimit = 1000

// portInUse reports whether a port is already listening on localhost. It aliases
// the session dial-probe so the allocator and the residue detector share one
// implementation; tests override it to exercise the exhaustion path.
var portInUse = portListening

// NextPort returns the next free Tilt port, starting from max-registered-port+1
// (minimum 10350) and skipping any candidate that is already a registered
// TiltPort or currently listening on localhost. Not race-safe on its own; the
// atomic allocate-and-register path goes through Register.
func NextPort() (int, error) {
	workspaces, err := Load()
	if err != nil {
		return 0, err
	}
	return nextPortFrom(workspaces)
}

// nextPortFrom computes the next free port against an already-loaded registry,
// so a caller holding the registry lock reserves without a second Load.
func nextPortFrom(workspaces []Workspace) (int, error) {
	used := make(map[int]bool, len(workspaces))
	max := minPort - 1
	for _, ws := range workspaces {
		used[ws.TiltPort] = true
		if ws.TiltPort > max {
			max = ws.TiltPort
		}
	}

	start := max + 1
	for candidate := start; candidate < start+portScanLimit; candidate++ {
		if used[candidate] || portInUse(candidate) {
			continue
		}
		return candidate, nil
	}
	return 0, fmt.Errorf("no free port found in range %d-%d", start, start+portScanLimit-1)
}
