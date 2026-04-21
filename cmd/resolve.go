package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"

	"devstack/internal/config"
	"devstack/internal/workspace"
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

	env, ok := ws.ResolveEnvironment(envName)
	if !ok {
		return nil, workspace.Environment{}, "", fmt.Errorf("environment %q not found in workspace %q. Run: devstack env list", envName, ws.Name)
	}

	return ws, env, envName, nil
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
