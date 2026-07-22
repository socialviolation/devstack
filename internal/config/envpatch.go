package config

import "fmt"

// ResolveEnvPatch merges the env patches active at the three scopes for a service
// instance: workspace (ws.Workspace.Env), service (m.Service.Env), and stack
// (stackEnv). An empty name at a scope applies no patch there. Each non-empty name
// must exist in the workspace's env catalog (ws.Envs) or resolution fails naming
// the missing env and scope. The patches merge workspace → service → stack, so a
// key set by a more specific scope wins (stack > service > workspace). The result
// is always non-nil, empty when no env is active.
func ResolveEnvPatch(ws *WorkspaceManifest, m *ServiceManifest, stackEnv string) (map[string]string, error) {
	scopes := []struct {
		name  string
		scope string
	}{
		{ws.Workspace.Env, "workspace"},
		{m.Service.Env, "service"},
		{stackEnv, "stack"},
	}

	merged := map[string]string{}
	for _, s := range scopes {
		if s.name == "" {
			continue
		}
		patch, ok := ws.Envs[s.name]
		if !ok {
			return nil, fmt.Errorf("env %q applied at %s scope is not defined in workspace envs", s.name, s.scope)
		}
		for k, v := range patch.Values {
			merged[k] = v
		}
	}
	return merged, nil
}
