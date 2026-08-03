// Package replica keeps the runnable copy of a workspace: one git worktree per
// service, detached at that service repo's default branch tip, generated from
// the user's checkout but never running out of it. The checkout is the template
// — the source of git objects, the workspace manifest, and machine-local
// gitignored config — so half-finished work parked there neither runs nor
// blocks, and nothing here is a place anyone edits.
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

// Worktree is one service's replica checkout.
type Worktree struct {
	Service      string
	Path         string
	Branch       string
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

// ServiceSync is what one service's worktree moved to, and where it was before.
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

// Root is the replica root for a workspace: a sibling of the checkout, never
// inside it, so a cwd resolves to exactly one of the two.
func Root(ws *workspace.Workspace) string {
	return filepath.Join(filepath.Dir(ws.Path), rootDirName, ws.Name)
}

// Ensure brings the replica in line with the template's manifest: a worktree for
// every service, none for a service the manifest dropped, and a generated
// workspace manifest describing them. It is idempotent — a second call with
// nothing changed creates nothing.
func Ensure(ws *workspace.Workspace) (*EnsureResult, error) {
	if ws == nil {
		return nil, fmt.Errorf("no workspace resolved")
	}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace %q at %s: %w", ws.Name, ws.Path, err)
	}

	root := Root(ws)
	if root == ws.Path || strings.HasPrefix(root, ws.Path+string(os.PathSeparator)) {
		return nil, fmt.Errorf("refusing: replica root %s would be nested under workspace %s (breaks workspace detection)", root, ws.Path)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("failed to create replica root %s: %w", root, err)
	}

	res := &EnsureResult{Root: root}
	names := serviceNames(template)
	paths := make(map[string]string, len(names))
	for _, name := range names {
		repoPath := template.Services[name].RepoPath
		path := filepath.Join(root, name)
		paths[name] = path

		if err := requireGitRepo(name, repoPath); err != nil {
			return nil, err
		}
		branch, ref, err := defaultRef(repoPath)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		wt := Worktree{Service: name, Path: path, Branch: branch}

		if _, err := os.Stat(path); err == nil {
			res.Existing = append(res.Existing, wt)
			continue
		}
		// Detached, never on a branch: the template checkout usually holds the
		// default branch, and git refuses to check one branch out twice.
		if _, err := git(repoPath, "worktree", "add", "--detach", path, ref); err != nil {
			return nil, fmt.Errorf("worktree for %q: %w", name, err)
		}
		materialized, err := worktree.MaterializeIgnoredConfig(repoPath, path)
		if err != nil {
			return nil, fmt.Errorf("materialize local config for %q: %w", name, err)
		}
		wt.Materialized = materialized
		res.Created = append(res.Created, wt)
	}

	removed, warnings := removeStale(root, names)
	res.Removed = removed
	res.Warnings = append(res.Warnings, warnings...)

	manifestPath, err := writeManifest(template, ws.Name, root, names, paths)
	if err != nil {
		return nil, err
	}
	res.ManifestPath = manifestPath
	return res, nil
}

// Sync moves every worktree to the current default branch tip and refreshes the
// machine-local config copied out of the template. A fetch that fails leaves the
// worktree synced to the local ref: being offline must not stop the replica from
// running.
func Sync(ws *workspace.Workspace) (*SyncResult, error) {
	if ws == nil {
		return nil, fmt.Errorf("no workspace resolved")
	}
	root := Root(ws)
	if !config.HasWorkspaceManifest(root) {
		return nil, notBuilt(ws, root)
	}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace %q at %s: %w", ws.Name, ws.Path, err)
	}

	res := &SyncResult{Root: root}
	for _, name := range serviceNames(template) {
		repoPath := template.Services[name].RepoPath
		path := filepath.Join(root, name)
		if _, err := os.Stat(path); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("service %q has no worktree at %s; run devstack workspace up to build it", name, path))
			continue
		}
		if err := requireGitRepo(name, repoPath); err != nil {
			return nil, err
		}

		if hasOrigin(path) {
			if _, err := git(path, "fetch", "origin"); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("fetch for %q failed, syncing to the local ref instead: %v", name, err))
			}
		}
		_, ref, err := defaultRef(repoPath)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		before, err := shortSHA(path)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		// --force because the replica's working tree is disposable: a plain
		// checkout refuses when materialized config collides with the new tip.
		if _, err := git(path, "checkout", "--force", "--detach", ref); err != nil {
			return nil, fmt.Errorf("sync worktree for %q: %w", name, err)
		}
		after, err := shortSHA(path)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		if _, err := worktree.MaterializeIgnoredConfigForce(repoPath, path); err != nil {
			return nil, fmt.Errorf("materialize local config for %q: %w", name, err)
		}

		res.Services = append(res.Services, ServiceSync{
			Service: name,
			Path:    path,
			Ref:     shortRef(ref),
			Before:  before,
			After:   after,
		})
	}
	return res, nil
}

// Resolve resolves the replica and folds in the template's environment
// definitions and workspace-scope env selection, since the replica inherits —
// never redefines — them.
//
// It folds in the observability and runtime blocks for a different reason: the
// generated manifest carries neither, because it is generated by the stack
// generator and a stack legitimately has neither of its own. The replica IS the
// workspace, so a caller reading them off it would read zero values and take a
// workspace with a collector configured for one without.
func Resolve(ws *workspace.Workspace) (*config.ResolvedWorkspace, error) {
	if ws == nil {
		return nil, fmt.Errorf("no workspace resolved")
	}
	root := Root(ws)
	if !config.HasWorkspaceManifest(root) {
		return nil, notBuilt(ws, root)
	}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace %q at %s: %w", ws.Name, ws.Path, err)
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

// ErrNotBuilt is what every "no replica yet" error wraps, so a caller that can
// carry on with the template can tell it from a manifest that will not resolve.
var ErrNotBuilt = errors.New("no replica built yet; run devstack workspace up to build it")

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
		return "", fmt.Errorf("failed to marshal replica manifest: %w", err)
	}
	path := config.WorkspaceManifestPath(root)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write replica manifest %s: %w", path, err)
	}
	return path, nil
}

func removeStale(root string, names []string) (removed []string, warnings []string) {
	keep := make(map[string]bool, len(names))
	for _, name := range names {
		keep[name] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, []string{fmt.Sprintf("failed to read replica root %s: %v", root, err)}
	}
	for _, e := range entries {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		path := filepath.Join(root, e.Name())
		// Forced: the replica holds no work anyone owns, so there is nothing to
		// refuse over.
		if err := worktree.Remove(path, true); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to remove worktree %s for dropped service %q: %v", path, e.Name(), err))
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, warnings
}

// defaultRef resolves the ref a worktree should sit on: the default branch as
// origin has it, falling back to the local branch when there is no remote copy.
func defaultRef(repoPath string) (branch, ref string, err error) {
	branch, err = gitinfo.DefaultBranch(repoPath)
	if err != nil {
		return "", "", err
	}
	remote := "refs/remotes/origin/" + branch
	if _, err := git(repoPath, "rev-parse", "--verify", "--quiet", remote); err == nil {
		return branch, remote, nil
	}
	local := "refs/heads/" + branch
	if _, err := git(repoPath, "rev-parse", "--verify", "--quiet", local); err == nil {
		return branch, local, nil
	}
	return "", "", fmt.Errorf("default branch %q exists in neither origin nor %s", branch, repoPath)
}

func shortRef(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/"), "refs/heads/")
}

func shortSHA(path string) (string, error) {
	return git(path, "rev-parse", "--short", "HEAD")
}

func hasOrigin(path string) bool {
	_, err := git(path, "remote", "get-url", "origin")
	return err == nil
}

func requireGitRepo(name, repoPath string) error {
	if _, err := git(repoPath, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("service %q at %s is not a git repository; the replica runs git worktrees, so every service must be a checkout", name, repoPath)
	}
	return nil
}

func serviceNames(rw *config.ResolvedWorkspace) []string {
	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
