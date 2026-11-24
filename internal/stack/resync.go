package stack

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
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
	Name      string
	Repos     []RepoResync
	Restart   []string
	Restarted []string
	Down      bool
}

type Restarter func(rec *Record, services []string) ([]string, error)

func Resync(base *workspace.Workspace, name string, restart Restarter) (*ResyncResult, error) {
	if base == nil {
		return nil, fmt.Errorf("devstack can not resolve the base workspace")
	}
	rec, err := Resolve(base.Name, name)
	if err != nil {
		return nil, err
	}
	repos, err := reposOf(rec)
	if err != nil {
		return nil, err
	}

	res := &ResyncResult{Name: rec.Name}
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
	if len(moved) == 0 {
		return res, nil
	}

	res.Restart, err = restartSet(rec, moved)
	if err != nil {
		return res, err
	}
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
		out.Status = ResyncUnchanged
		out.Reason = "a detached caller repo. Resync moves only the repos on the stack's branch"
		return out
	}
	if current != branch {
		out.Branch = current
		out.Reason = fmt.Sprintf("the worktree is on branch %s, not on the stack's branch %s", current, branch)
		return out
	}

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

func restartSet(rec *Record, moved map[string]bool) ([]string, error) {
	topo, err := config.BuildTopology(rec.Root)
	if err != nil {
		return nil, fmt.Errorf("can not read the topology of stack %q: %w", rec.Name, err)
	}
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
	return out, nil
}
