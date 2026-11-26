package stack

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

const (
	ResyncUpdated   = "updated"
	ResyncUnchanged = "unchanged"
	ResyncRefused   = "refused"
)

type RepoResync struct {
	Repo     string
	Branch   string
	Old      string
	New      string
	Status   string
	Reason   string
	Services []string
}

type ResyncResult struct {
	Name       string
	Branch     string
	Repos      []RepoResync
	Restart    []string
	Restarted  []string
	Down       bool
	Outside    []OutsideRepo
	Missing    []OverlayMember
	Migrations []string
	Notes      []string
}

type OutsideRepo struct {
	Repo     string
	Services []string
}

var remoteTimeout = 10 * time.Second

type Restarter func(rec *Record, services []string) ([]string, error)

func Resync(base *workspace.Workspace, name string, restart Restarter) (*ResyncResult, error) {
	if base == nil {
		return nil, fmt.Errorf("devstack can not resolve the workspace")
	}
	rec, err := Resolve(base.Name, name)
	if err != nil {
		return nil, err
	}
	repos, err := reposOf(rec)
	if err != nil {
		return nil, err
	}

	res := &ResyncResult{Name: rec.Name, Branch: rec.Branch}
	moved := map[string]bool{}
	for _, r := range repos {
		out := resyncRepo(r.top, rec.Branch)
		out.Repo = r.name
		out.Services = r.services
		if out.Status == ResyncUpdated {
			for _, s := range r.services {
				moved[s] = true
			}
		}
		res.Repos = append(res.Repos, out)
	}

	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return res, fmt.Errorf("can not resolve the workspace: %w", err)
	}
	stackRW, err := config.ResolveWorkspace(rec.Root)
	if err != nil {
		return res, fmt.Errorf("can not read the services of stack %q: %w", rec.Name, err)
	}
	topo, err := config.BuildTopology(rec.Root)
	if err != nil {
		return res, fmt.Errorf("can not read the topology of stack %q: %w", rec.Name, err)
	}
	res.Missing = missingNeeds(topo, baseRW, rec)
	res.Migrations, res.Notes = migrationsTouched(stackRW, rec, res.Repos)
	outside, notes := outsideRepos(baseRW, base.Path, rec)
	res.Outside = outside
	res.Notes = append(res.Notes, notes...)

	if len(moved) == 0 {
		return res, nil
	}
	res.Restart = restartSet(topo, rec, moved)
	if !rec.Active {
		res.Down = true
		return res, nil
	}
	res.Restarted, err = restart(rec, res.Restart)
	return res, err
}

type stackRepo struct {
	name     string
	top      string
	services []string
}

func reposOf(rec *Record) ([]stackRepo, error) {
	byTop := map[string]*stackRepo{}
	for svc, path := range rec.Worktrees {
		top, err := worktree.Toplevel(path)
		if err != nil {
			return nil, fmt.Errorf("the worktree of %s: %w", svc, err)
		}
		r, ok := byTop[top]
		if !ok {
			name := top
			if root, err := filepath.EvalSymlinks(rec.Root); err == nil {
				if rel, err := filepath.Rel(root, top); err == nil && !strings.HasPrefix(rel, "..") {
					name = rel
				}
			}
			r = &stackRepo{name: name, top: top}
			byTop[top] = r
		}
		r.services = append(r.services, svc)
	}
	out := make([]stackRepo, 0, len(byTop))
	for _, r := range byTop {
		sort.Strings(r.services)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func resyncRepo(top, branch string) RepoResync {
	out := RepoResync{Branch: branch, Status: ResyncRefused}
	current, err := worktree.CurrentBranch(top)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	old, err := worktree.Head(top)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Old, out.New = old, old
	if current == "" {
		def, err := gitinfo.DefaultBranch(top)
		if err != nil {
			out.Reason = err.Error()
			return out
		}
		out.Branch = def
		return forward(top, out, fmt.Sprintf("origin has no default branch %s, so this detached repo stays where it is", def))
	}
	if current != branch {
		out.Branch = current
		out.Reason = fmt.Sprintf("the worktree is on branch %s, not on the stack's branch %s", current, branch)
		return out
	}
	return forward(top, out, "")
}

func forward(top string, out RepoResync, noRemote string) RepoResync {
	branch, old := out.Branch, out.Old
	files, err := worktree.UncommittedFiles(top)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	if len(files) > 0 {
		out.Reason = "the worktree has uncommitted changes: " + strings.Join(files, ", ")
		return out
	}

	found, err := worktree.FetchBranch(top, branch)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	if !found {
		if noRemote != "" {
			out.Status = ResyncUnchanged
			out.Reason = noRemote
			return out
		}
		out.Reason = fmt.Sprintf("origin has no branch %s. Push it first", branch)
		return out
	}
	remoteRef := "origin/" + branch
	remote, err := worktree.RevParse(top, "refs/remotes/"+remoteRef)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	if remote == old {
		out.Status = ResyncUnchanged
		return out
	}
	behind, err := worktree.IsAncestor(top, old, remote)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	if !behind {
		ahead, err := worktree.IsAncestor(top, remote, old)
		if err != nil {
			out.Reason = err.Error()
			return out
		}
		if ahead {
			out.Status = ResyncUnchanged
			out.Reason = fmt.Sprintf("the worktree at %s is ahead of %s at %s", old, remoteRef, remote)
			return out
		}
		out.Reason = fmt.Sprintf("the worktree at %s and %s at %s diverged, and resync only fast-forwards", old, remoteRef, remote)
		return out
	}
	if err := worktree.FastForward(top, remoteRef); err != nil {
		out.Reason = err.Error()
		return out
	}
	out.New, err = worktree.Head(top)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Status = ResyncUpdated
	return out
}

func restartSet(topo *config.TopologyGraph, rec *Record, moved map[string]bool) []string {
	want := map[string]bool{}
	var visit func(string)
	visit = func(s string) {
		if want[s] {
			return
		}
		want[s] = true
		if svc, ok := topo.Services[s]; ok {
			for _, d := range svc.Dependents {
				visit(d)
			}
		}
	}
	for s := range moved {
		visit(s)
	}
	var out []string
	for _, s := range StartOrder(rec) {
		if want[s] {
			out = append(out, s)
		}
	}
	return out
}

func missingNeeds(topo *config.TopologyGraph, baseRW *config.ResolvedWorkspace, rec *Record) []OverlayMember {
	members := stringSet(rec.Members)
	closure, chains := topo.NeedsClosure(rec.Members, baseRW.SharedSet())
	var out []OverlayMember
	for _, s := range closure {
		if _, known := baseRW.Services[s]; members[s] || !known {
			continue
		}
		out = append(out, OverlayMember{Service: s, Reason: ReasonNeeds, Chain: chains[s]})
	}
	return out
}

func migrationsTouched(stackRW *config.ResolvedWorkspace, rec *Record, repos []RepoResync) (services, notes []string) {
	for _, r := range repos {
		if r.Status != ResyncUpdated {
			continue
		}
		for _, svc := range r.Services {
			s, ok := stackRW.Services[svc]
			if !ok || s.Manifest == nil || s.Manifest.Migrations == "" {
				continue
			}
			files, err := worktree.ChangedPaths(rec.Worktrees[svc], r.Old, r.New, s.Manifest.Migrations)
			if err != nil {
				notes = append(notes, fmt.Sprintf("devstack can not check the migrations of %s: %v", svc, err))
				continue
			}
			if len(files) > 0 {
				services = append(services, svc)
			}
		}
	}
	sort.Strings(services)
	return services, notes
}

func outsideRepos(baseRW *config.ResolvedWorkspace, root string, rec *Record) ([]OutsideRepo, []string) {
	if rec.Branch == "" {
		return nil, nil
	}
	members := stringSet(rec.Members)
	inStack := map[string]bool{}
	byTop := map[string][]string{}
	for name, svc := range baseRW.Services {
		top, err := worktree.Toplevel(svc.RepoPath)
		if err != nil {
			continue
		}
		if members[name] {
			inStack[top] = true
			continue
		}
		if !baseRW.IsShared(name) {
			byTop[top] = append(byTop[top], name)
		}
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}

	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		outside []OutsideRepo
		notes   []string
	)
	for top, services := range byTop {
		if inStack[top] {
			continue
		}
		name := top
		if rel, err := filepath.Rel(root, top); err == nil && !strings.HasPrefix(rel, "..") {
			name = rel
		}
		sort.Strings(services)
		wg.Add(1)
		go func(top, name string, services []string) {
			defer wg.Done()
			found, err := worktree.RemoteHasBranch(ctx, top, rec.Branch)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				notes = append(notes, fmt.Sprintf("devstack can not ask origin of %s for branch %s: %v", name, rec.Branch, err))
			case found:
				outside = append(outside, OutsideRepo{Repo: name, Services: services})
			}
		}(top, name, services)
	}
	wg.Wait()
	sort.Slice(outside, func(i, j int) bool { return outside[i].Repo < outside[j].Repo })
	sort.Strings(notes)
	return outside, notes
}

func BehindWarnings(rec *Record, fix string) (warnings, notes []string) {
	repos, err := reposOf(rec)
	if err != nil {
		return nil, []string{err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, r := range repos {
		wg.Add(1)
		go func(r stackRepo) {
			defer wg.Done()
			line, note := behindLine(ctx, r, rec.Branch, fix)
			mu.Lock()
			defer mu.Unlock()
			if line != "" {
				warnings = append(warnings, line)
			}
			if note != "" {
				notes = append(notes, note)
			}
		}(r)
	}
	wg.Wait()
	sort.Strings(warnings)
	sort.Strings(notes)
	return warnings, notes
}

func behindLine(ctx context.Context, r stackRepo, stackBranch, fix string) (line, note string) {
	branch, err := worktree.CurrentBranch(r.top)
	if err != nil {
		return "", err.Error()
	}
	kind := "on branch"
	switch {
	case branch == "":
		if branch, err = gitinfo.DefaultBranch(r.top); err != nil {
			return "", err.Error()
		}
		kind = "detached, against the default branch"
	case branch != stackBranch:
		return "", ""
	}
	st, err := worktree.BehindOrigin(ctx, r.top, branch)
	if err != nil {
		return "", fmt.Sprintf("devstack can not check %s against origin/%s: %v", r.name, branch, err)
	}
	if !st.Behind {
		return "", ""
	}
	by := ""
	switch {
	case st.Commits == 1:
		by = " by 1 commit"
	case st.Commits > 1:
		by = fmt.Sprintf(" by %d commits", st.Commits)
	}
	return fmt.Sprintf("%s (%s %s) is behind origin%s. %s", r.name, kind, branch, by, fix), ""
}
