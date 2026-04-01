package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace represents a registered development workspace.
type Workspace struct {
	Name     string `json:"name"`
	Path     string `json:"path"`      // absolute path to workspace root (e.g. /home/nick/dev/navexa)
	TiltPort int    `json:"tilt_port"` // port Tilt API listens on for this workspace

	// SigNoz port overrides. Zero means use the default.
	OtelUIPort       int `json:"otel_ui_port,omitempty"`        // SigNoz UI + query API (default 3301)
	OtelOTLPGRPCPort int `json:"otel_otlp_grpc_port,omitempty"` // OTLP gRPC (default 4317)
	OtelOTLPHTTPPort int `json:"otel_otlp_http_port,omitempty"` // OTLP HTTP (default 4318)
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

// DataDir returns the runtime data directory for a named workspace.
func DataDir(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".local", "share", "devstack", name) + "/"
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

// Register adds or updates a workspace in the registry.
// If a workspace with the same path already exists, it is updated in place.
// If TiltPort is 0, a port is auto-assigned starting from 10350.
func Register(ws Workspace) error {
	ws.Path = filepath.Clean(expandPath(ws.Path))

	workspaces, err := Load()
	if err != nil {
		return err
	}

	// Auto-assign port if not specified
	if ws.TiltPort == 0 {
		port, err := NextPort()
		if err != nil {
			return err
		}
		ws.TiltPort = port
	}

	// Check for duplicate by path — update if exists
	for i, existing := range workspaces {
		if existing.Path == ws.Path {
			workspaces[i] = ws
			return Save(workspaces)
		}
	}

	// Append new workspace
	workspaces = append(workspaces, ws)
	return Save(workspaces)
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

// DetectFromCwd detects the workspace that contains the current working directory.
// Returns an error if the cwd is not inside any registered workspace.
func DetectFromCwd() (*Workspace, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}

	workspaces, err := Load()
	if err != nil {
		return nil, err
	}

	for _, ws := range workspaces {
		if cwd == ws.Path || strings.HasPrefix(cwd, ws.Path+"/") {
			w := ws
			return &w, nil
		}
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

// OtelOTLPEndpoint returns the OTLP HTTP endpoint services should push to.
func OtelOTLPEndpoint(ws *Workspace) string {
	return fmt.Sprintf("http://localhost:%d", ws.HTTPPort())
}

// OtelQueryEndpoint returns the SigNoz query API base URL used by MCP tools.
func OtelQueryEndpoint(ws *Workspace) string {
	return fmt.Sprintf("http://localhost:%d", ws.UIPort())
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

// NextPort returns the next available Tilt port (max existing port + 1, minimum 10350).
// If no workspaces are registered, returns 10350.
func NextPort() (int, error) {
	workspaces, err := Load()
	if err != nil {
		return 0, err
	}

	const minPort = 10350
	max := minPort - 1
	for _, ws := range workspaces {
		if ws.TiltPort > max {
			max = ws.TiltPort
		}
	}

	if max < minPort {
		return minPort, nil
	}
	return max + 1, nil
}
