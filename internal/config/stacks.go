package config

import (
	"fmt"
	"sort"
)

// Overlay reasons, in the order they win: a service named on the command line is
// "changed" even if it is also a caller of another named service.
const (
	OverlayReasonChanged = "changed"
	OverlayReasonCaller  = "caller"
	OverlayReasonNeeded  = "needed"
)

// OverlaySet returns the services a stack runs its own copy of, and why each one
// is in the set. It is the named services, the services that call them, and
// everything those two need: what they start after, and what they call. A stack
// stands up what it needs, so a copy talks to the stack's own sibling and not to
// base's.
//
// The closure does not walk back up from a need. A member's needs come into the
// overlay. The other callers of that need do not.
func OverlaySet(g *TopologyGraph, changed []string) ([]string, map[string]string, error) {
	reasons := map[string]string{}
	for _, name := range changed {
		if _, ok := g.Services[name]; !ok {
			return nil, nil, fmt.Errorf("the topology does not know the changed service %q", name)
		}
		reasons[name] = OverlayReasonChanged
	}
	for _, name := range changed {
		for _, caller := range g.TransitiveCallers(name) {
			if reasons[caller] == "" {
				reasons[caller] = OverlayReasonCaller
			}
		}
	}

	seeds := make([]string, 0, len(reasons))
	for name := range reasons {
		seeds = append(seeds, name)
	}
	for _, name := range seeds {
		for _, need := range g.TransitiveNeeds(name) {
			// BuildTopology keeps an edge to a service it can not find, and
			// reports it as an issue. There is no repository to cut a worktree
			// from, so the overlay leaves it out.
			if _, ok := g.Services[need]; !ok {
				continue
			}
			if reasons[need] == "" {
				reasons[need] = OverlayReasonNeeded
			}
		}
	}

	overlay := make([]string, 0, len(reasons))
	for name := range reasons {
		overlay = append(overlay, name)
	}
	sort.Strings(overlay)
	return overlay, reasons, nil
}

func GenerateStackManifest(base *ResolvedWorkspace, stackName string, overlay []string, pathFor func(string) string) (*WorkspaceManifest, error) {
	if base == nil || base.Manifest == nil {
		return nil, fmt.Errorf("devstack can not resolve the base workspace")
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
			return nil, fmt.Errorf("the overlay service %q has no worktree path", name)
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
		Groups:       filterEdges(base.Manifest.Groups, inOverlay, false),
		Dependencies: filterEdges(base.Manifest.Dependencies, inOverlay, true),
		Calls:        filterEdges(base.Manifest.Calls, inOverlay, true),
		StartsAfter:  filterEdges(base.Manifest.StartsAfter, inOverlay, true),
	}
	return manifest, nil
}

func filterEdges(in map[string][]string, inOverlay map[string]bool, keyIsService bool) map[string][]string {
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
			if inOverlay[v] {
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
