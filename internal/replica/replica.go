// Package replica keeps the runnable copy of a workspace: one git worktree per
// repository, detached at that repository's default branch tip, generated from
// the user's checkout but never running out of it. A repository that holds
// several services gets one worktree, and each service is a directory in it.
// The checkout is the template — the source of git objects, the workspace
// manifest, and machine-local gitignored config — so half-finished work parked
// there neither runs nor blocks, and nothing here is a place anyone edits.
package replica

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

const rootDirName = ".devstack-base"

// Worktree is one repository's worktree in the replica. It carries the services
// that live in it, because a repository can hold more than one.
type Worktree struct {
	Repo         string
	Path         string
	Branch       string
	Services     []string
	Materialized []string
}

type EnsureResult struct {
	Root         string
	ManifestPath string
	Created      []Worktree
	Existing     []Worktree
	Removed      []string
	Warnings     []string
}

type ServiceSync struct {
	Service string
	Path    string
	Ref     string
	Before  string
	After   string
}

type SyncResult struct {
	Root     string
	Services []ServiceSync
	Warnings []string
}

// A sibling of the checkout, never inside it, so a directory belongs to at most
// one of the two.
func Root(ws *workspace.Workspace) string {
	return filepath.Join(filepath.Dir(ws.Path), rootDirName, ws.Name)
}

func DetectFromCwd() (*workspace.Workspace, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("can not read the current directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	all, err := workspace.All()
	if err != nil {
		return nil, err
	}
	for i := range all {
		root := Root(&all[i])
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		if cwd == root || strings.HasPrefix(cwd, root+string(os.PathSeparator)) {
			w := all[i]
			return &w, nil
		}
	}
	return nil, fmt.Errorf("this directory is not inside a replica")
}

func Ensure(ws *workspace.Workspace) (*EnsureResult, error) {
	if ws == nil {
		return nil, fmt.Errorf("devstack can not resolve the workspace")
	}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil, fmt.Errorf("can not resolve the workspace %q at %s: %w", ws.Name, ws.Path, err)
	}

	root := Root(ws)
	if root == ws.Path || strings.HasPrefix(root, ws.Path+string(os.PathSeparator)) {
		return nil, fmt.Errorf("devstack can not use the replica root %s. The root is under workspace %s, and a nested root breaks workspace detection", root, ws.Path)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("can not create the replica root %s: %w", root, err)
	}

	res := &EnsureResult{Root: root}
	names := serviceNames(template)
	repos, err := plan(template, names)
	if err != nil {
		return nil, err
	}

	paths := make(map[string]string, len(names))
	keep := make(map[string]bool, len(repos))
	for _, r := range repos {
		path := r.Path(root)
		keep[r.Dir] = true
		for _, name := range r.Services {
			paths[name] = r.ServicePath(root, name)
		}

		branch, ref, err := gitinfo.DefaultRef(r.Toplevel)
		if err != nil {
			return nil, fmt.Errorf("repository %s: %w", r.Toplevel, err)
		}
		wt := Worktree{Repo: r.Dir, Path: path, Branch: branch, Services: r.Services}

		if _, err := os.Stat(path); err == nil {
			res.Existing = append(res.Existing, wt)
			continue
		}
		// Detached, never on a branch: the template checkout usually holds the
		// default branch, and git refuses to check one branch out twice.
		if _, err := addWorktree(r.Toplevel, path, ref); err != nil {
			return nil, fmt.Errorf("worktree for the repository %s: %w", r.Dir, err)
		}
		materialized, err := worktree.MaterializeIgnoredConfig(r.Toplevel, path)
		if err != nil {
			return nil, fmt.Errorf("can not materialize the local configuration for the repository %s: %w", r.Dir, err)
		}
		wt.Materialized = materialized
		res.Created = append(res.Created, wt)
	}

	removed, warnings := removeStale(root, keep)
	res.Removed = removed
	res.Warnings = append(res.Warnings, warnings...)

	manifestPath, err := writeManifest(template, ws.Name, root, names, paths)
	if err != nil {
		return nil, err
	}
	res.ManifestPath = manifestPath
	return res, nil
}

// A fetch that fails syncs to the local ref instead: being offline must not stop
// the replica from running.
func Sync(ws *workspace.Workspace) (*SyncResult, error) {
	if ws == nil {
		return nil, fmt.Errorf("devstack can not resolve the workspace")
	}
	root := Root(ws)
	if !config.HasWorkspaceManifest(root) {
		return nil, notBuilt(ws, root)
	}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil, fmt.Errorf("can not resolve the workspace %q at %s: %w", ws.Name, ws.Path, err)
	}

	repos, err := plan(template, serviceNames(template))
	if err != nil {
		return nil, err
	}

	res := &SyncResult{Root: root}
	for _, r := range repos {
		path := r.Path(root)
		if _, err := os.Stat(path); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the services %s have no worktree at %s. To build it, run devstack workspace up", strings.Join(r.Services, ", "), path))
			continue
		}

		if hasOrigin(path) {
			if _, err := git(path, "fetch", "origin"); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("the fetch for the repository %q failed, so devstack syncs to the local ref instead: %v", r.Dir, err))
			}
		}
		_, ref, err := gitinfo.DefaultRef(r.Toplevel)
		if err != nil {
			return nil, fmt.Errorf("repository %s: %w", r.Toplevel, err)
		}
		before, err := shortSHA(path)
		if err != nil {
			return nil, fmt.Errorf("repository %s: %w", r.Dir, err)
		}
		// --force because the replica's working tree is disposable: a plain
		// checkout refuses when materialized config collides with the new tip.
		if _, err := git(path, "checkout", "--force", "--detach", ref); err != nil {
			return nil, fmt.Errorf("can not sync the worktree for the repository %q: %w", r.Dir, err)
		}
		after, err := shortSHA(path)
		if err != nil {
			return nil, fmt.Errorf("repository %s: %w", r.Dir, err)
		}
		if _, err := worktree.MaterializeIgnoredConfigForce(r.Toplevel, path); err != nil {
			return nil, fmt.Errorf("can not materialize the local configuration for the repository %q: %w", r.Dir, err)
		}

		for _, name := range r.Services {
			res.Services = append(res.Services, ServiceSync{
				Service: name,
				Path:    r.ServicePath(root, name),
				Ref:     gitinfo.ShortRef(ref),
				Before:  before,
				After:   after,
			})
		}
	}
	sort.Slice(res.Services, func(i, j int) bool { return res.Services[i].Service < res.Services[j].Service })
	return res, nil
}

// plan groups the workspace's services by the repository that holds them. git
// cuts a worktree of a whole repository, so a repository that holds several
// services in subdirectories gets one worktree, and each service is a directory
// in it.
func plan(template *config.ResolvedWorkspace, names []string) ([]worktree.Repo, error) {
	return worktree.Plan(names, func(s string) string { return template.Services[s].RepoPath }, nil)
}

// The replica's manifest comes from the stack generator, which carries no
// environments, observability or runtime block because a stack owns none of its
// own. The replica is the workspace, so all three are folded back in: without
// that a caller reads zero values and takes an instrumented workspace for a bare
// one.
func Resolve(ws *workspace.Workspace) (*config.ResolvedWorkspace, error) {
	if ws == nil {
		return nil, fmt.Errorf("devstack can not resolve the workspace")
	}
	root := Root(ws)
	if !config.HasWorkspaceManifest(root) {
		return nil, notBuilt(ws, root)
	}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil, fmt.Errorf("can not resolve the workspace %q at %s: %w", ws.Name, ws.Path, err)
	}
	rw, err := config.ResolveWorkspace(root)
	if err != nil {
		return nil, err
	}
	rw.Manifest.Environments = template.Manifest.Environments
	rw.Manifest.Observability = template.Manifest.Observability
	rw.Manifest.Runtime = template.Manifest.Runtime
	if rw.Manifest.Workspace.Env == "" {
		rw.Manifest.Workspace.Env = template.Manifest.Workspace.Env
	}
	return rw, nil
}

// Wrapped by every "no replica yet" error, so a caller that can carry on with
// the template can tell it from a manifest that will not resolve.
var ErrNotBuilt = errors.New("devstack has not built the replica. To build it and start its services, run devstack workspace up")

func notBuilt(ws *workspace.Workspace, root string) error {
	return fmt.Errorf("workspace %q at %s: %w", ws.Name, root, ErrNotBuilt)
}

func writeManifest(template *config.ResolvedWorkspace, name, root string, names []string, paths map[string]string) (string, error) {
	// The replica is the workspace, so it keeps the workspace's name: downstream
	// resource naming must not shift when base starts running out of here.
	manifest, err := config.GenerateStackManifest(template, name, names, func(s string) string { return paths[s] })
	if err != nil {
		return "", err
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("can not encode the replica manifest: %w", err)
	}
	path := config.WorkspaceManifestPath(root)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("can not write the replica manifest %s: %w", path, err)
	}
	return path, nil
}

// removeStale drops a worktree directory that no repository in the manifest
// claims. keep holds the directory name of every repository the replica needs.
func removeStale(root string, keep map[string]bool) (removed []string, warnings []string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, []string{fmt.Sprintf("devstack can not read the replica root %s: %v", root, err)}
	}
	for _, e := range entries {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		path := filepath.Join(root, e.Name())
		// Forced: the replica holds no work anyone owns, so there is nothing to
		// refuse over.
		if err := worktree.Remove(path, true); err != nil {
			warnings = append(warnings, fmt.Sprintf("devstack can not remove the worktree %s. The manifest no longer lists the repository %q: %v", path, e.Name(), err))
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, warnings
}

func shortSHA(path string) (string, error) {
	return git(path, "rev-parse", "--short", "HEAD")
}

func hasOrigin(path string) bool {
	_, err := git(path, "remote", "get-url", "origin")
	return err == nil
}

func serviceNames(rw *config.ResolvedWorkspace) []string {
	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// git says this when the destination is gone but its admin record is not. The
// text is the only signal git gives: the exit status is 128 for every failure.
const alreadyRegistered = "already registered worktree"

// addWorktree adds the replica worktree, and prunes the source repository once
// if git rejects the add because the deleted destination is still registered.
// `rm -rf` on the replica root is the documented way to rebuild it, and it
// leaves that record behind in every source repository.
func addWorktree(repo, path, ref string) (string, error) {
	out, err := git(repo, "worktree", "add", "--detach", path, ref)
	if err == nil || !strings.Contains(err.Error(), alreadyRegistered) {
		return out, err
	}
	if err := worktree.Prune(repo); err != nil {
		return "", err
	}
	return git(repo, "worktree", "add", "--detach", path, ref)
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s in %s failed: %w\n%s", strings.Join(args, " "), dir, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
