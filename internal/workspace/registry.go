package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/socialviolation/devstack/internal/config"
)

// ObservabilityConfig holds the connection config for an observability backend.
type ObservabilityConfig struct {
	Backend string `json:"backend"` // "signoz" (default and only supported value)
	URL     string `json:"url"`     // Base URL, e.g. "http://localhost:3301"
	APIKey  string `json:"api_key,omitempty"`
}

// Workspace represents a registered development workspace.
type Workspace struct {
	Name     string `json:"name"`
	Path     string `json:"path"`      // absolute path to workspace root (e.g. /home/nick/dev/navexa)
	TiltPort int    `json:"tilt_port"` // port Tilt API listens on for this workspace

	// OtelPlugin names the active OTEL plugin (default "openobserve").
	OtelPlugin string `json:"otel_plugin,omitempty"`
	// OtelPluginConfig holds plugin-specific configuration key-value pairs.
	OtelPluginConfig map[string]string `json:"otel_plugin_config,omitempty"`

	// Tunnel remote defaults for `devstack tunnel push/pull`. Machine-specific, so
	// they live in the per-user registry rather than the committed project config.
	TunnelHost string `json:"tunnel_host,omitempty"`
	TunnelUser string `json:"tunnel_user,omitempty"`
	// TunnelLast is what the last successful push or pull forwarded.
	TunnelLast *TunnelForward `json:"tunnel_last,omitempty"`

	// Active reports whether this workspace's services are folded into the one
	// host Tilt daemon. `devstack workspace up` sets it, `down` clears it.
	Active bool `json:"active,omitempty"`
}

// OverlayProjectConfig overlays the workspace manifest's observability backend
// and settings onto the registry entry, letting the committed project config
// take precedence over the per-machine registry.
func (ws *Workspace) OverlayProjectConfig() {
	if ws.Path == "" {
		return
	}
	obs := config.WorkspaceObservability(ws.Path)
	if backend := obs.ResolvedBackend(); backend != "" {
		ws.OtelPlugin = backend
	}
	if len(obs.Settings) > 0 {
		merged := make(map[string]string, len(ws.OtelPluginConfig)+len(obs.Settings))
		for k, v := range ws.OtelPluginConfig {
			merged[k] = v
		}
		for k, v := range obs.Settings {
			merged[k] = v
		}
		ws.OtelPluginConfig = merged
	}
	warnStrandedLegacyOtelConfig(ws.Path, obs)
}

// legacyOtelWarned tracks the workspace paths already warned about, so a single
// command run emits the deprecation notice at most once per workspace.
var legacyOtelWarned sync.Map

var warnWriter io.Writer = os.Stderr

const baseConfigureCmd = "devstack otel configure"

// warnStrandedLegacyOtelConfig reports otel settings left in the retired
// .devstack.json project store that the manifest does not carry, naming the
// command that re-applies them. Without this a workspace that kept its upstream
// there would quietly fall back to the collector's debug mode.
func warnStrandedLegacyOtelConfig(wsPath string, obs config.WorkspaceManifestObservability) {
	plugin, settings := config.LegacyOtelSettings(wsPath)
	var stranded []string
	for k, v := range settings {
		if obs.Settings[k] != v {
			stranded = append(stranded, k)
		}
	}
	sort.Strings(stranded)
	pluginStranded := plugin != "" && plugin != obs.Backend
	if !pluginStranded && len(stranded) == 0 {
		return
	}
	if _, loaded := legacyOtelWarned.LoadOrStore(wsPath, true); loaded {
		return
	}

	cmd := baseConfigureCmd
	if pluginStranded {
		cmd += " --plugin=" + plugin
	}
	var secrets []string
	for _, k := range stranded {
		if config.IsCredentialKey(k) {
			secrets = append(secrets, k)
			continue
		}
		cmd += fmt.Sprintf(" --set %s=%s", k, settings[k])
	}

	fmt.Fprintf(warnWriter, "warning: %s holds otel config devstack no longer reads — it now lives in %s.\n",
		filepath.Join(wsPath, ".devstack.json"), config.WorkspaceManifestFileName)
	if cmd != baseConfigureCmd {
		fmt.Fprintf(warnWriter, "         re-apply it with: %s\n", cmd)
	}
	for _, k := range secrets {
		fmt.Fprintf(warnWriter, "         %q is a credential: supply it through the environment (.envrc), not a committed file.\n", k)
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

// OTLP ingestion is machine-level: one collector serves every workspace, which
// is why these are fixed rather than per-workspace.
const OTLPGRPCPort = 4317
const OTLPHTTPPort = 4318

// OtelOTLPEndpoint returns the OTLP gRPC endpoint services should push to.
func OtelOTLPEndpoint(ws *Workspace) string {
	return fmt.Sprintf("http://localhost:%d", OTLPGRPCPort)
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

// TunnelForward describes a forward that ran, in enough detail to repeat it.
// `tunnel restart` reads it so a re-established tunnel matches what was up,
// rather than what the flag defaults describe — the direction and the stack
// mapping are otherwise lost the moment the command exits.
type TunnelForward struct {
	Mode     string `json:"mode"`
	Services string `json:"services,omitempty"`
	Stacks   bool   `json:"stacks,omitempty"`
	AsBase   string `json:"as_base,omitempty"`
	Otel     bool   `json:"otel,omitempty"`
}

// UpdateTunnelForward records what a workspace's last push or pull forwarded.
func UpdateTunnelForward(name string, fwd TunnelForward) error {
	workspaces, err := Load()
	if err != nil {
		return err
	}
	for i, ws := range workspaces {
		if strings.EqualFold(ws.Name, name) {
			workspaces[i].TunnelLast = &fwd
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
