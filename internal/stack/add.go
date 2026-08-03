package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

type AddInput struct {
	Base    *workspace.Workspace
	Name    string
	Members []string
	// From is the ref the new worktrees are cut from. Empty resolves each
	// service repo's default branch, as Create does.
	From string
}

type AddResult struct {
	StackName      string
	StackRoot      string
	BaseName       string
	Branch         string
	Groups         []string
	Added          []OverlayMember
	AlreadyPresent []string
	Overlay        []string
	Worktrees      []WorktreeResult
	ManifestPath   string
	// Ports holds only what this call allocated. The ports the stack already
	// held are unchanged and are not repeated here.
	Ports map[string]int
	// Active reports that the stack's resources are in the host Tiltfile, so the
	// caller must regenerate it for the added copies to exist.
	Active   bool
	Warnings []string
}

// Add puts more services into a stack that already exists, leaving everything
// already in it exactly where it is: same worktrees, same branch tips, same
// ports. It does not start the added services, and it does not change whether
// the stack is up.
func Add(in AddInput) (*AddResult, error) {
	base := in.Base
	if base == nil {
		return nil, fmt.Errorf("no base workspace resolved")
	}
	rec, err := FindStack(base.Name, in.Name)
	if err != nil {
		return nil, err
	}
	if len(in.Members) == 0 {
		return nil, fmt.Errorf("no services given: name the service(s) or group(s) to add to stack %q", rec.Name)
	}

	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base workspace: %w", err)
	}
	topo, err := config.BuildTopology(base.Path)
	if err != nil {
		return nil, err
	}
	named, groups, err := expandGroups(topo, base.Name, in.Members)
	if err != nil {
		return nil, err
	}

	present := stringSet(rec.Overlay)
	var changed, already []string
	for _, s := range named {
		if present[s] {
			already = append(already, s)
			continue
		}
		changed = append(changed, s)
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("stack %q already overlays %s: nothing to add\nsee what it runs: devstack stack list", rec.Name, strings.Join(already, ", "))
	}

	pulled, err := config.OverlaySet(topo, changed)
	if err != nil {
		return nil, err
	}
	changedSet := stringSet(changed)
	var adding []string
	for _, s := range pulled {
		if !present[s] {
			adding = append(adding, s)
		}
	}
	overlay := append(append([]string(nil), rec.Overlay...), adding...)
	sort.Strings(overlay)

	res := &AddResult{
		StackName:      rec.FullName(),
		StackRoot:      rec.Root,
		BaseName:       rec.Base,
		Branch:         rec.Branch,
		AlreadyPresent: already,
		Overlay:        overlay,
		Active:         rec.Active,
		Ports:          map[string]int{},
	}
	for _, s := range adding {
		reason := "caller"
		if changedSet[s] {
			reason = "changed"
		}
		res.Added = append(res.Added, OverlayMember{Service: s, Reason: reason})
	}

	worktreePaths := map[string]string{}
	for s, p := range rec.Worktrees {
		worktreePaths[s] = p
	}
	if err := os.MkdirAll(rec.Root, 0755); err != nil {
		return nil, fmt.Errorf("failed to create stack root %s: %w", rec.Root, err)
	}

	var leftBehind []string
	for _, s := range adding {
		svc, ok := baseRW.Services[s]
		if !ok {
			return nil, fmt.Errorf("service %q is not in base workspace %q", s, base.Name)
		}
		path := filepath.Join(rec.Root, s)
		ref := in.From
		if ref == "" {
			_, resolved, err := gitinfo.DefaultRef(svc.RepoPath)
			if err != nil {
				return nil, fmt.Errorf("service %q: %w; name a ref to cut from with --from", s, err)
			}
			ref = resolved
		}
		wt, err := worktree.Create(svc.RepoPath, path, rec.Branch, ref, changedSet[s])
		if err != nil {
			return nil, fmt.Errorf("worktree for %q: %w", s, err)
		}
		wr := WorktreeResult{Service: s, Path: path, Ref: gitinfo.ShortRef(ref), Dirty: wt.SourceDirty}
		if changedSet[s] {
			wr.Branch = rec.Branch
		} else {
			wr.Detached = true
		}
		materialized, err := worktree.MaterializeIgnoredConfig(svc.RepoPath, path)
		if err != nil {
			return nil, fmt.Errorf("materialize local config for %q: %w", s, err)
		}
		wr.Materialized = materialized
		res.Worktrees = append(res.Worktrees, wr)
		worktreePaths[s] = path
		if wt.SourceDirty || wt.SourceOffRef {
			leftBehind = append(leftBehind, fmt.Sprintf("%s (%s)", wr.Ref, s))
		}
	}
	if len(leftBehind) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("worktrees were cut from %s, not from the base checkout — uncommitted work, and commits the checkout holds beyond that ref, are not in this stack",
			strings.Join(leftBehind, ", ")))
	}

	manifest, err := config.GenerateStackManifest(baseRW, rec.FullName(), overlay, func(s string) string { return worktreePaths[s] })
	if err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal stack manifest: %w", err)
	}
	res.ManifestPath = config.WorkspaceManifestPath(rec.Root)
	if err := os.WriteFile(res.ManifestPath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write stack manifest: %w", err)
	}

	keys := portKeys(baseRW, adding)
	ports, err := workspace.AllocateAdditionalPorts(rec.RuntimeKey(), keys)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate service ports: %w", err)
	}
	for _, k := range keys {
		res.Ports[k] = ports[k]
	}

	rec.Overlay = overlay
	rec.Worktrees = worktreePaths
	rec.Ports = ports
	rec.Groups = mergeGroups(rec.Groups, groups)
	res.Groups = rec.Groups
	if err := upsertStack(*rec); err != nil {
		return nil, fmt.Errorf("failed to record stack: %w", err)
	}

	return res, nil
}

// portKeys lists the qualified port keys the given services declare, in the
// order the allocator should hand them out.
func portKeys(rw *config.ResolvedWorkspace, services []string) []string {
	var keys []string
	for _, s := range services {
		svc, ok := rw.Services[s]
		if !ok || svc.Manifest == nil {
			continue
		}
		declared := make([]string, 0, len(svc.Manifest.Ports))
		for k := range svc.Manifest.Ports {
			declared = append(declared, k)
		}
		sort.Strings(declared)
		for _, k := range declared {
			keys = append(keys, QualifyPortKey(s, k))
		}
	}
	return keys
}

func mergeGroups(have, add []string) []string {
	seen := stringSet(have)
	out := append([]string(nil), have...)
	for _, g := range add {
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}
