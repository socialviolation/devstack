package config

import "fmt"

// ResolveEnvPatch merges the active environments' Values by scope (stack beats
// service beats workspace). An unknown env name at any scope is an error.
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
		env, ok := ws.Environments[s.name]
		if !ok {
			return nil, fmt.Errorf("env %q applied at %s scope is not defined in workspace environments", s.name, s.scope)
		}
		for k, v := range env.Values {
			merged[k] = v
		}
	}
	return merged, nil
}
