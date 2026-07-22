package config

import "path/filepath"

// EnvDir is where the service's command runs, and therefore where its .envrc and
// env.files resolve from. Writers and readers of a service's env must agree on
// it: runtime.workDir moves the whole env ladder, .envrc included.
func (s ResolvedService) EnvDir() string {
	if s.Manifest == nil {
		return s.RepoPath
	}
	wd := s.Manifest.Runtime.WorkDir
	if wd == "" || wd == "." {
		return s.RepoPath
	}
	if filepath.IsAbs(wd) {
		return filepath.Clean(wd)
	}
	return filepath.Join(s.RepoPath, wd)
}

// EnvRung names one layer of the env precedence ladder.
type EnvRung string

// The ladder, lowest rung first. A later rung overrides an earlier one.
const (
	RungEnvrc           EnvRung = ".envrc"
	RungWorkspaceFiles  EnvRung = "workspace env.files"
	RungServiceFiles    EnvRung = "service env.files"
	RungWorkspaceValues EnvRung = "workspace env.values"
	RungServiceValues   EnvRung = "service env.values"
	RungActiveEnv       EnvRung = "active env"
	RungManaged         EnvRung = "devstack-computed"
)

// EnvLayer is one resolved rung. Source names the file it came from, where the
// rung does not already identify it.
type EnvLayer struct {
	Rung   EnvRung
	Source string
	Values map[string]string
}

type envFileRef struct {
	name string
	rung EnvRung
}

// EnvLadder resolves the env precedence ladder for a service, lowest rung first:
// .envrc, workspace env.files, service env.files, workspace env.values, service
// env.values, and finally devstack's own computed values. Env files are executed
// rather than line-parsed, so conditionals and ${VAR:-default} resolve as the
// developer's shell resolves them; dir is both where they are looked up and
// where the service's command runs. The active-env rung, resolved from the envs
// applied at the workspace/service/stack scopes (stackEnv names the stack scope),
// sits just above service env.values and below devstack's computed values.
func EnvLadder(dir string, ws *WorkspaceManifest, m *ServiceManifest, stackEnv string, managed map[string]string) ([]EnvLayer, error) {
	envrc, err := ResolveEnvrc(dir)
	if err != nil {
		return nil, err
	}
	layers := []EnvLayer{{Rung: RungEnvrc, Source: EnvrcFileName, Values: envrc}}

	refs := make([]envFileRef, 0, len(ws.Env.Files)+len(m.Env.Files))
	for _, f := range ws.Env.Files {
		refs = append(refs, envFileRef{name: f, rung: RungWorkspaceFiles})
	}
	for _, f := range m.Env.Files {
		refs = append(refs, envFileRef{name: f, rung: RungServiceFiles})
	}

	seen := map[string]bool{EnvrcFileName: true}
	for _, ref := range refs {
		if ref.name == "" || seen[ref.name] {
			continue
		}
		seen[ref.name] = true
		vals, err := ResolveEnvFile(dir, ref.name)
		if err != nil {
			return nil, err
		}
		layers = append(layers, EnvLayer{Rung: ref.rung, Source: ref.name, Values: vals})
	}

	activeEnv, err := ResolveEnvPatch(ws, m, stackEnv)
	if err != nil {
		return nil, err
	}

	return append(layers,
		EnvLayer{Rung: RungWorkspaceValues, Source: WorkspaceManifestFileName, Values: ws.Env.Values},
		EnvLayer{Rung: RungServiceValues, Source: ServiceManifestFileName, Values: m.Env.Values},
		EnvLayer{Rung: RungActiveEnv, Source: ActiveEnvName(ws.Workspace.Env, m.Service.Env, stackEnv), Values: activeEnv},
		EnvLayer{Rung: RungManaged, Values: managed},
	), nil
}

// MergeEnvLadder flattens the ladder into the env a service actually receives.
func MergeEnvLadder(layers []EnvLayer) map[string]string {
	out := map[string]string{}
	for _, l := range layers {
		for k, v := range l.Values {
			out[k] = v
		}
	}
	return out
}

// OverriderOf returns the highest layer above rung that defines key, and whether
// one exists. A value written at rung cannot reach the service when it does.
func OverriderOf(layers []EnvLayer, rung EnvRung, key string) (EnvLayer, bool) {
	above := false
	var winner EnvLayer
	found := false
	for _, l := range layers {
		if above {
			if _, ok := l.Values[key]; ok {
				winner = l
				found = true
			}
		}
		if l.Rung == rung {
			above = true
		}
	}
	return winner, found
}
