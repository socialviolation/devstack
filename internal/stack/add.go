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
	// Promoted holds the services that were already in the stack on a detached
	// HEAD and are now on the stack's branch.
	Promoted     []string
	Overlay      []string
	Worktrees    []WorktreeResult
	ManifestPath string
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
		return nil, fmt.Errorf("devstack can not resolve the base workspace")
	}
	rec, err := FindStack(base.Name, in.Name)
	if err != nil {
		return nil, err
	}
	if len(in.Members) == 0 {
		return nil, fmt.Errorf("no services given: name the services or the groups to add to stack %q", rec.Name)
	}

	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return nil, fmt.Errorf("can not resolve the base workspace: %w", err)
	}
	topo, err := config.BuildTopology(base.Path)
	if err != nil {
		return nil, err
	}
	named, groups, err := expandGroups(topo, base.Name, in.Members)
	if err != nil {
		return nil, err
	}

	held, err := readHeldWorktrees(rec, baseRW)
	if err != nil {
		return nil, err
	}

	present := stringSet(rec.Overlay)
	var changed, already, promote []string
	for _, s := range named {
		if !present[s] {
			changed = append(changed, s)
			continue
		}
		branch, err := held.branchOf(s)
		if err != nil {
			return nil, err
		}
		if branch == rec.Branch {
			already = append(already, s)
			continue
		}
		promote = append(promote, s)
	}
	if len(changed) == 0 && len(promote) == 0 {
		return nil, fmt.Errorf("stack %q already overlays %s on branch %s, so there is nothing to add\nto see what it runs, run: devstack stack list", rec.Name, strings.Join(already, ", "), rec.Branch)
	}

	var adding []string
	changedSet := stringSet(changed)
	if len(changed) > 0 {
		pulled, err := config.OverlaySet(topo, changed)
		if err != nil {
			return nil, err
		}
		for _, s := range pulled {
			if !present[s] {
				adding = append(adding, s)
			}
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
		return nil, fmt.Errorf("can not create the stack root %s: %w", rec.Root, err)
	}

	repos, err := worktree.Plan(adding, func(s string) string { return baseRW.Services[s].RepoPath }, held.dirs)
	if err != nil {
		return nil, err
	}

	// A failed add must leave nothing behind, so the retry does not die on
	// "already exists", and the stack the caller had before the add is the stack
	// they have after it. That covers the worktrees this call cut, the branch it
	// put a held worktree on, and the manifest it wrote. The worktrees the stack
	// already holds are never removed.
	var built []string
	var promotions []promotion
	var manifestBefore []byte
	manifestWritten := false
	unwind := func() {
		for i := len(built) - 1; i >= 0; i-- {
			_ = worktree.Remove(built[i], true)
		}
		if manifestWritten {
			if manifestBefore == nil {
				_ = os.Remove(res.ManifestPath)
			} else {
				_ = os.WriteFile(res.ManifestPath, manifestBefore, 0644)
			}
		}
		for _, p := range promotions {
			_ = p.restore()
		}
	}

	// A caller was pulled into the stack on a detached HEAD, so nobody can
	// commit in it. Putting its worktree on the stack's branch is the only way
	// to commit there that does not destroy the stack and cut it again.
	promoted, promotions, err := held.promote(promote, rec.Branch)
	if err != nil {
		unwind()
		return nil, err
	}
	res.Promoted = promoted

	var leftBehind []string
	for _, r := range repos {
		repoWorktree, reused := held.roots[r.Toplevel]
		var materialized []string
		ref := in.From
		if !reused {
			repoWorktree = r.Path(rec.Root)
			if _, err := os.Stat(repoWorktree); err == nil {
				unwind()
				return nil, fmt.Errorf("devstack can not build the worktree of the repository %s. The path %s already exists, and the record of stack %q does not know it. An earlier add did not finish.\nRemove the directory, then run the command again:\n  rm -rf %s\nIf git still records a worktree there, run: git -C %s worktree prune",
					r.Dir, repoWorktree, rec.Name, repoWorktree, r.Toplevel)
			}
			if ref == "" {
				_, resolved, err := gitinfo.DefaultRef(r.Toplevel)
				if err != nil {
					unwind()
					return nil, fmt.Errorf("repository %s: %w. Name a ref to cut from with --from", r.Dir, err)
				}
				ref = resolved
			}
			wt, err := worktree.Create(r.Toplevel, repoWorktree, rec.Branch, ref, r.Changed(changedSet))
			if err != nil {
				unwind()
				return nil, fmt.Errorf("worktree for the repository %q: %w", r.Dir, err)
			}
			built = append(built, repoWorktree)
			materialized, err = worktree.MaterializeIgnoredConfig(r.Toplevel, repoWorktree)
			if err != nil {
				unwind()
				return nil, fmt.Errorf("can not materialize the local configuration for the repository %q: %w", r.Dir, err)
			}
			if wt.SourceDirty || wt.SourceOffRef {
				leftBehind = append(leftBehind, fmt.Sprintf("%s (%s)", gitinfo.ShortRef(ref), r.Dir))
			}
		}

		branch, err := worktree.CurrentBranch(repoWorktree)
		if err != nil {
			unwind()
			return nil, fmt.Errorf("devstack can not read the branch of the worktree %s: %w", repoWorktree, err)
		}
		for i, s := range r.Services {
			path := filepath.Join(repoWorktree, r.Rel[s])
			worktreePaths[s] = path
			wr := WorktreeResult{
				Service:  s,
				Path:     path,
				Repo:     filepath.Base(repoWorktree),
				RepoPath: repoWorktree,
				Branch:   branch,
				Ref:      gitinfo.ShortRef(ref),
				Detached: branch == "",
			}
			if i == 0 {
				wr.Materialized = materialized
			}
			res.Worktrees = append(res.Worktrees, wr)
		}
	}
	sort.Slice(res.Worktrees, func(i, j int) bool { return res.Worktrees[i].Service < res.Worktrees[j].Service })
	if len(leftBehind) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("devstack cut the worktrees from %s, not from the base checkout. This stack does not have the uncommitted work in the checkout. This stack does not have the commits that the checkout holds beyond that ref.",
			strings.Join(leftBehind, ", ")))
	}
	res.Warnings = append(res.Warnings, sharedRepoWarnings(baseRW, repos, overlay, rec.Name)...)

	manifest, err := config.GenerateStackManifest(baseRW, rec.FullName(), overlay, func(s string) string { return worktreePaths[s] })
	if err != nil {
		unwind()
		return nil, err
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		unwind()
		return nil, fmt.Errorf("can not encode the stack manifest: %w", err)
	}
	res.ManifestPath = config.WorkspaceManifestPath(rec.Root)
	if before, err := os.ReadFile(res.ManifestPath); err == nil {
		manifestBefore = before
	}
	manifestWritten = true
	if err := os.WriteFile(res.ManifestPath, data, 0644); err != nil {
		unwind()
		return nil, fmt.Errorf("can not write the stack manifest: %w", err)
	}

	keys := portKeys(baseRW, adding)
	ports, err := workspace.AllocateAdditionalPorts(rec.RuntimeKey(), keys)
	if err != nil {
		unwind()
		return nil, fmt.Errorf("can not allocate the service ports: %w", err)
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
		unwind()
		return nil, fmt.Errorf("can not record the stack: %w", err)
	}

	return res, nil
}

// heldWorktrees is what a stack already has on disk, read per repository. A
// stack cuts one worktree for each repository, so two services of one
// repository share a worktree, a branch, and a promotion.
type heldWorktrees struct {
	roots  map[string]string   // base repository toplevel -> stack worktree root
	ofSvc  map[string]string   // service -> stack worktree root
	inRoot map[string][]string // stack worktree root -> the stack's services in it
	dirs   map[string]bool     // directory names in use below the stack root
}

func readHeldWorktrees(rec *Record, baseRW *config.ResolvedWorkspace) (*heldWorktrees, error) {
	h := &heldWorktrees{
		roots:  map[string]string{},
		ofSvc:  map[string]string{},
		inRoot: map[string][]string{},
		dirs:   map[string]bool{},
	}
	for _, s := range rec.Overlay {
		path := rec.Worktrees[s]
		if path == "" {
			return nil, fmt.Errorf("stack %q has no worktree recorded for the service %q. The record is incomplete, so devstack can not add to it. Remove the stack and create it again", rec.Name, s)
		}
		root, err := worktree.Toplevel(path)
		if err != nil {
			return nil, fmt.Errorf("devstack can not read the worktree of %q at %s: %w", s, path, err)
		}
		h.ofSvc[s] = root
		h.inRoot[root] = append(h.inRoot[root], s)
		h.dirs[filepath.Base(root)] = true
		if svc, ok := baseRW.Services[s]; ok {
			if top, err := worktree.Toplevel(svc.RepoPath); err == nil {
				h.roots[top] = root
			}
		}
	}
	return h, nil
}

func (h *heldWorktrees) branchOf(service string) (string, error) {
	root, ok := h.ofSvc[service]
	if !ok {
		return "", fmt.Errorf("devstack has no worktree recorded for the service %q", service)
	}
	branch, err := worktree.CurrentBranch(root)
	if err != nil {
		return "", fmt.Errorf("devstack can not read the branch of the worktree %s: %w", root, err)
	}
	return branch, nil
}

// promotion is where one worktree stood before Add put it on the stack's
// branch. That worktree holds work somebody owns, so a failed add puts it back.
type promotion struct {
	root   string
	branch string
	commit string
}

// restore puts the worktree back on the branch it had, or back on the commit it
// had when it was on no branch.
func (p promotion) restore() error {
	if p.branch != "" {
		return worktree.Attach(p.root, p.branch)
	}
	return worktree.Detach(p.root, p.commit)
}

// promote puts the worktree of each named service on branch and reports every
// service that moved. One worktree can hold several services, and all of them
// move together, so the report names them all.
//
// It also returns what each worktree it moved stood on before. A move that fails
// halfway still returns the moves it made, because the caller has to undo them.
func (h *heldWorktrees) promote(services []string, branch string) ([]string, []promotion, error) {
	if len(services) == 0 {
		return nil, nil, nil
	}
	moved := map[string]bool{}
	var undo []promotion
	for _, s := range services {
		root, ok := h.ofSvc[s]
		if !ok {
			return nil, undo, fmt.Errorf("devstack has no worktree recorded for the service %q", s)
		}
		if moved[root] {
			continue
		}
		was, err := worktree.CurrentBranch(root)
		if err != nil {
			return nil, undo, fmt.Errorf("devstack can not read the branch of the worktree %s: %w", root, err)
		}
		commit, err := worktree.Head(root)
		if err != nil {
			return nil, undo, fmt.Errorf("devstack can not read the commit of the worktree %s: %w", root, err)
		}
		if err := worktree.Attach(root, branch); err != nil {
			return nil, undo, fmt.Errorf("devstack can not put the worktree %s on branch %s: %w", root, branch, err)
		}
		undo = append(undo, promotion{root: root, branch: was, commit: commit})
		moved[root] = true
	}

	var out []string
	for root := range moved {
		out = append(out, h.inRoot[root]...)
	}
	sort.Strings(out)
	return out, undo, nil
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
