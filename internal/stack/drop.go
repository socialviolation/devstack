package stack

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

type DropInput struct {
	Base     *workspace.Workspace
	Name     string
	Services []string
	Teardown func(removed []string)
}

type KeptWorktree struct {
	Service       string
	Path          string
	RemoveCommand string
}

type DropResult struct {
	StackName     string
	Namespace     string
	Removed       []string
	Overlay       []string
	Kept          []KeptWorktree
	ReleasedPorts []string
	ManifestPath  string
	Active        bool
}

func Drop(in DropInput) (*DropResult, error) {
	base := in.Base
	if base == nil {
		return nil, fmt.Errorf("devstack can not resolve the base workspace")
	}
	rec, err := FindStack(base.Name, in.Name)
	if err != nil {
		return nil, err
	}
	if len(in.Services) == 0 {
		return nil, fmt.Errorf("no services given: name the services to remove from stack %q", rec.Name)
	}

	present := stringSet(rec.Overlay)
	removing := map[string]bool{}
	for _, s := range in.Services {
		if !present[s] {
			return nil, fmt.Errorf("stack %q does not run %q. It runs: %s", rec.Name, s, strings.Join(rec.Overlay, ", "))
		}
		removing[s] = true
	}
	var removed []string
	remaining := []string{}
	for _, s := range rec.Overlay {
		if removing[s] {
			removed = append(removed, s)
		} else {
			remaining = append(remaining, s)
		}
	}

	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return nil, fmt.Errorf("can not resolve the base workspace: %w", err)
	}
	topo, err := config.BuildTopology(base.Path)
	if err != nil {
		return nil, err
	}
	var blocked []string
	for _, s := range remaining {
		for _, d := range baseRW.Manifest.ResourceDeps(s) {
			if removing[d] {
				blocked = append(blocked, fmt.Sprintf("%s depends on %s", s, d))
			}
		}
	}
	if len(blocked) > 0 {
		return nil, fmt.Errorf("devstack can not remove %s from stack %q: %s. Remove the dependents too, or keep it",
			strings.Join(removed, ", "), rec.Name, strings.Join(blocked, "; "))
	}
	if in.Teardown != nil {
		in.Teardown(removed)
	}

	worktreePaths := map[string]string{}
	for _, s := range remaining {
		worktreePaths[s] = rec.Worktrees[s]
	}
	manifest, err := config.GenerateStackManifest(baseRW, rec.FullName(), remaining, func(s string) string { return worktreePaths[s] })
	if err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("can not encode the stack manifest: %w", err)
	}
	manifestPath := config.WorkspaceManifestPath(rec.Root)
	before, readErr := os.ReadFile(manifestPath)
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return nil, fmt.Errorf("can not write the stack manifest: %w", err)
	}

	var keys []string
	ports := map[string]int{}
	for k, v := range rec.Ports {
		if svc, _, ok := splitPortKey(k); ok && removing[svc] {
			keys = append(keys, k)
			continue
		}
		ports[k] = v
	}
	sort.Strings(keys)

	keptRoots := map[string]bool{}
	for _, s := range remaining {
		if root, err := worktree.Toplevel(rec.Worktrees[s]); err == nil {
			keptRoots[root] = true
		}
	}

	res := &DropResult{
		StackName:     rec.FullName(),
		Namespace:     rec.Name,
		Removed:       removed,
		Overlay:       remaining,
		ReleasedPorts: keys,
		ManifestPath:  manifestPath,
		Active:        rec.Active,
	}
	for _, s := range removed {
		kept := KeptWorktree{Service: s, Path: rec.Worktrees[s]}
		if root, err := worktree.Toplevel(kept.Path); err == nil && !keptRoots[root] {
			kept.RemoveCommand = fmt.Sprintf("git -C %s worktree remove %s", baseRW.Services[s].RepoPath, root)
		}
		res.Kept = append(res.Kept, kept)
	}

	rec.Overlay = remaining
	rec.Worktrees = worktreePaths
	rec.Ports = ports
	rec.Groups = groupsStillCovered(rec.Groups, topo.Groups, remaining)
	if err := upsertStack(*rec); err != nil {
		if readErr == nil {
			_ = os.WriteFile(manifestPath, before, 0644)
		}
		return nil, fmt.Errorf("can not record the stack: %w", err)
	}
	if _, err := workspace.ReleasePortKeys(rec.RuntimeKey(), keys); err != nil {
		return res, fmt.Errorf("devstack removed %s from stack %q, but it can not release their ports: %w", strings.Join(removed, ", "), rec.Name, err)
	}
	return res, nil
}

func groupsStillCovered(groups []string, members map[string][]string, overlay []string) []string {
	in := stringSet(overlay)
	var out []string
	for _, g := range groups {
		for _, m := range members[g] {
			if in[m] {
				out = append(out, g)
				break
			}
		}
	}
	return out
}
