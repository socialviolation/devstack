package cmd

import (
	"fmt"

	"github.com/spf13/viper"

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
