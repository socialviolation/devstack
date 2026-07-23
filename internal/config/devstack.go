package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WorkspaceConfig holds the devstack configuration for a workspace.
type WorkspaceConfig struct {
	Deps         map[string][]string `json:"deps"`          // service -> list of deps
	Groups       map[string][]string `json:"groups"`        // group name -> list of services
	ServicePaths map[string]string   `json:"service_paths"` // service -> git repo root path
}

const configFileName = ".devstack.json"

// Load resolves workspace config from the new manifest model when present,
// falling back to the legacy .devstack.json file during migration.
func Load(workspacePath string) (*WorkspaceConfig, error) {
	if HasWorkspaceManifest(workspacePath) {
		resolved, err := ResolveWorkspace(workspacePath)
		if err != nil {
			return nil, err
		}
		return resolved.ToLegacyConfig(), nil
	}
	return loadLegacyConfig(workspacePath)
}

// LegacyOtelSettings returns the backend name and plugin settings stranded in
// <workspacePath>/.devstack.json, the retired project store. devstack no longer
// reads them as config; they are surfaced so a user can re-apply them to the
// workspace manifest. Returns zero values when the file is absent or carries none.
func LegacyOtelSettings(workspacePath string) (string, map[string]string) {
	data, err := os.ReadFile(filepath.Join(workspacePath, configFileName))
	if err != nil {
		return "", nil
	}
	var legacy struct {
		OtelPlugin       string            `json:"otel_plugin"`
		OtelPluginConfig map[string]string `json:"otel_plugin_config"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return "", nil
	}
	return legacy.OtelPlugin, legacy.OtelPluginConfig
}

// ResolveDeps performs a BFS topological sort returning an ordered list of services
// to start: deps first, then the service itself.
// Returns an error if a cycle is detected.
// If service has no deps, returns [service].
func ResolveDeps(cfg *WorkspaceConfig, service string) ([]string, error) {
	// Track the order services should be started (deps first)
	var ordered []string
	visited := map[string]bool{}
	inStack := map[string]bool{}

	var visit func(s string) error
	visit = func(s string) error {
		if inStack[s] {
			return fmt.Errorf("dependency cycle detected involving service %q", s)
		}
		if visited[s] {
			return nil
		}
		inStack[s] = true
		for _, dep := range cfg.Deps[s] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		inStack[s] = false
		visited[s] = true
		ordered = append(ordered, s)
		return nil
	}

	if err := visit(service); err != nil {
		return nil, err
	}

	return ordered, nil
}
