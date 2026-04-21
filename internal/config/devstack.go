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

	// OTEL plugin config — persisted here so settings travel with the project.
	OtelPlugin       string            `json:"otel_plugin,omitempty"`
	OtelPluginConfig map[string]string `json:"otel_plugin_config,omitempty"`
}

const configFileName = ".devstack.json"

// Load reads <workspacePath>/.devstack.json.
// Returns an empty config (not an error) if the file doesn't exist.
func Load(workspacePath string) (*WorkspaceConfig, error) {
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

	// Ensure maps are not nil
	if cfg.Deps == nil {
		cfg.Deps = map[string][]string{}
	}
	if cfg.Groups == nil {
		cfg.Groups = map[string][]string{}
	}
	if cfg.ServicePaths == nil {
		cfg.ServicePaths = map[string]string{}
	}

	return &cfg, nil
}

// Save writes <workspacePath>/.devstack.json with JSON indentation.
func Save(workspacePath string, cfg *WorkspaceConfig) error {
	path := filepath.Join(workspacePath, configFileName)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal devstack config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write devstack config: %w", err)
	}
	return nil
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
