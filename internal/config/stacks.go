package config

import (
	"fmt"
	"sort"
)

func GenerateStackManifest(base *ResolvedWorkspace, stackName string, overlay []string, pathFor func(string) string) (*WorkspaceManifest, error) {
	if base == nil || base.Manifest == nil {
		return nil, fmt.Errorf("devstack can not resolve the workspace")
	}

	inOverlay := map[string]bool{}
	for _, name := range overlay {
		inOverlay[name] = true
	}

	members := make([]string, 0, len(inOverlay))
	for name := range inOverlay {
		members = append(members, name)
	}
	sort.Strings(members)

	repos := make([]string, 0, len(members))
	for _, name := range members {
		path := pathFor(name)
		if path == "" {
			return nil, fmt.Errorf("the stack member %q has no worktree path", name)
		}
		repos = append(repos, path)
	}

	manifest := &WorkspaceManifest{
		Version: base.Manifest.Version,
		Workspace: WorkspaceManifestWorkspace{
			Name: stackName,
			RepoDiscovery: WorkspaceManifestRepoDiscovery{
				Mode:  RepoDiscoveryModeExplicit,
				Repos: repos,
			},
		},
		Runtime: WorkspaceManifestRuntime{
			Orchestrator: base.Manifest.Runtime.Orchestrator,
		},
		Env:          cloneWorkspaceEnv(base.Manifest.Env),
		Groups:       filterEdges(base.Manifest.Groups, inOverlay, nil, false),
		Dependencies: filterEdges(base.Manifest.Dependencies, inOverlay, base.SharedSet(), true),
		Calls:        filterEdges(base.Manifest.Calls, inOverlay, base.SharedSet(), true),
		StartsAfter:  filterEdges(base.Manifest.StartsAfter, inOverlay, base.SharedSet(), true),
		Partial:      true,
	}
	return manifest, nil
}

func filterEdges(in map[string][]string, inOverlay, shared map[string]bool, keyIsService bool) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string][]string{}
	for key, values := range in {
		if keyIsService && !inOverlay[key] {
			continue
		}
		kept := make([]string, 0, len(values))
		for _, v := range values {
			if inOverlay[v] || shared[v] {
				kept = append(kept, v)
			}
		}
		if len(kept) == 0 {
			continue
		}
		out[key] = kept
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneWorkspaceEnv(in WorkspaceManifestEnv) WorkspaceManifestEnv {
	out := WorkspaceManifestEnv{}
	if len(in.Values) > 0 {
		out.Values = make(map[string]string, len(in.Values))
		for k, v := range in.Values {
			out.Values[k] = v
		}
	}
	if len(in.Files) > 0 {
		out.Files = append([]string(nil), in.Files...)
	}
	return out
}
