package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

// resolveWorkspaceAndEnv resolves the active workspace and environment from Viper config.
// Workspace is detected from cwd if --workspace flag / DEVSTACK_WORKSPACE not set.
// Environment defaults to "local" if DEVSTACK_ENVIRONMENT / --env not set.
func resolveWorkspaceAndEnv() (*workspace.Workspace, workspace.Environment, string, error) {
	wsFlag := viper.GetString("workspace")
	envName := viper.GetString("environment")
	if envName == "" {
		envName = "local"
	}

	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return nil, workspace.Environment{}, "", err
	}

	env, ok := resolveActiveEnv(ws, envName)
	if !ok {
		return nil, workspace.Environment{}, "", fmt.Errorf("environment %q not found in workspace %q. Run: devstack env list", envName, ws.Name)
	}

	return ws, env, envName, nil
}

// resolveActiveEnv resolves envName for ws, preferring the workspace manifest's
// environments: map. A manifest env maps onto the workspace.Environment shape;
// the "local" default is synthesised from legacy fields when not defined. When
// the workspace has no manifest it falls back to the legacy registry lookup.
func resolveActiveEnv(ws *workspace.Workspace, envName string) (workspace.Environment, bool) {
	if config.HasWorkspaceManifest(ws.Path) {
		if m, err := config.LoadWorkspaceManifest(ws.Path); err == nil {
			if we, ok := m.Environments[envName]; ok {
				return manifestEnvToWorkspace(we), true
			}
			if envName == "local" {
				return ws.ResolveEnvironment("local")
			}
			return workspace.Environment{}, false
		}
	}
	return ws.ResolveEnvironment(envName)
}

// manifestEnvToWorkspace maps a manifest environment definition onto the
// workspace.Environment shape the runtime commands consume. An empty type
// defaults to local. The manifest carries no API key, so APIKey is left unset.
func manifestEnvToWorkspace(we config.WorkspaceEnvironment) workspace.Environment {
	t := workspace.EnvironmentType(we.Type)
	if t == "" {
		t = workspace.EnvironmentTypeLocal
	}
	return workspace.Environment{
		Type: t,
		Observability: workspace.ObservabilityConfig{
			Backend:      we.Observability.Backend,
			URL:          we.Observability.URL,
			OTLPEndpoint: we.Observability.OTLPEndpoint,
		},
	}
}

// allEnvironments returns every named environment for ws, preferring the
// workspace manifest's environments: map (always including a synthesised
// "local"). Falls back to the legacy registry set when there is no manifest.
func allEnvironments(ws *workspace.Workspace) map[string]workspace.Environment {
	if config.HasWorkspaceManifest(ws.Path) {
		if m, err := config.LoadWorkspaceManifest(ws.Path); err == nil {
			result := map[string]workspace.Environment{}
			if local, ok := ws.ResolveEnvironment("local"); ok {
				result["local"] = local
			}
			for name, we := range m.Environments {
				result[name] = manifestEnvToWorkspace(we)
			}
			return result
		}
	}
	return ws.AllEnvironments()
}

// requireLocalEnv returns an error if the active environment is not local.
// Use this to guard Tilt-dependent commands (stop, restart, configure, process_logs).
func requireLocalEnv(envName string, env workspace.Environment) error {
	if env.Type != workspace.EnvironmentTypeLocal {
		return fmt.Errorf("this command requires a local environment; %q is %s (read-only)\nUse DEVSTACK_ENVIRONMENT=local or omit --env to target the local dev stack", envName, env.Type)
	}
	return nil
}

// detectServicesFromCwd resolves services from the current directory using the new
// manifest-aware resolver, falling back to service path matching for legacy workspaces.
func detectServicesFromCwd(workspacePath string, cfg *config.WorkspaceConfig) ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}

	ctx, err := config.ResolveContext(config.ResolveOptions{StartPath: cwd, WorkspacePath: workspacePath})
	if err == nil && ctx.CurrentService.Value != "" {
		return []string{ctx.CurrentService.Value}, nil
	}

	var matches []string
	for name, path := range cfg.ServicePaths {
		if cwd == path || strings.HasPrefix(cwd, path+"/") {
			matches = append(matches, name)
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("must specify a service name or group name\nUsage: devstack start <service>\n       devstack start <group>")
	}
	return matches, nil
}

// detectServiceFromCwd returns the single service matching the cwd.
// Errors if multiple match — use detectServicesFromCwd for that case.
func detectServiceFromCwd(workspacePath string, cfg *config.WorkspaceConfig) (string, error) {
	matches, err := detectServicesFromCwd(workspacePath, cfg)
	if err != nil {
		return "", err
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple services match (%s); please specify explicitly", strings.Join(matches, ", "))
	}
	return matches[0], nil
}

// resolveTarget resolves a name to a list of services using this priority:
// 1. Exact match in cfg.ServicePaths → returns []string{name}
// 2. Exact match in cfg.Groups → returns the group's member list
// 3. Returns error with helpful message
// If name is empty, falls back to cwd auto-detection (detectServicesFromCwd).
func resolveTarget(workspacePath, name string, cfg *config.WorkspaceConfig) ([]string, error) {
	if name == "" {
		return detectServicesFromCwd(workspacePath, cfg)
	}

	// Check service name first
	if _, ok := cfg.ServicePaths[name]; ok {
		return []string{name}, nil
	}

	// Check group name
	if members, ok := cfg.Groups[name]; ok {
		return members, nil
	}

	return nil, fmt.Errorf("'%s' is not a known service or group\nRun 'devstack services' to see available services or 'devstack groups' to see groups", name)
}
