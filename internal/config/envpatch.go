package config

import "fmt"

// ResolveEnvPatch merges the active environments' Values by scope (stack beats
// service beats workspace). An unknown env name at any scope is an error.
func ResolveEnvPatch(ws *WorkspaceManifest, m *ServiceManifest, stackEnv string) (map[string]string, error) {
	layers, err := ActiveEnvLayers(ws, m, stackEnv)
	if err != nil {
		return nil, err
	}
	merged := map[string]string{}
	for _, l := range layers {
		for k, v := range l.Values {
			merged[k] = v
		}
	}
	return merged, nil
}

// ActiveEnvLayers returns one layer per env scope that is set, in the order they
// override each other (workspace, then service, then stack), each carrying only
// its own env's Values so a key can be attributed to the env that supplied it.
// An unknown env name at any scope is an error.
func ActiveEnvLayers(ws *WorkspaceManifest, m *ServiceManifest, stackEnv string) ([]EnvLayer, error) {
	scopes := []struct {
		name  string
		scope string
	}{
		{ws.Workspace.Env, "workspace"},
		{m.Service.Env, "service"},
		{stackEnv, "stack"},
	}

	var layers []EnvLayer
	for _, s := range scopes {
		if s.name == "" {
			continue
		}
		env, ok := ws.Environments[s.name]
		if !ok {
			return nil, fmt.Errorf("env %q applied at %s scope is not defined in workspace environments", s.name, s.scope)
		}
		layers = append(layers, EnvLayer{Rung: RungActiveEnv, Source: s.name, Values: env.Values})
	}
	return layers, nil
}

// ActiveEnvName returns the name of the effective environment for an instance:
// the most-specific non-empty scope, stack beating service beating workspace.
func ActiveEnvName(wsEnv, svcEnv, stackEnv string) string {
	if stackEnv != "" {
		return stackEnv
	}
	if svcEnv != "" {
		return svcEnv
	}
	return wsEnv
}
