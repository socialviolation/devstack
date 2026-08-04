package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Repo is one git repository that holds one or more of a workspace's services.
//
// A repository can hold several services in subdirectories. git cuts a worktree
// of a whole repository and never of a subdirectory, so the unit of a worktree
// is the repository. Every other unit — the port, the record, the overlay
// membership, the group and the hook — stays the service.
type Repo struct {
	Toplevel string
	Dir      string
	Services []string
	// Rel maps a service to its path below the repository root. A service at the
	// repository root maps to ".".
	Rel map[string]string
}

// Path is the worktree root of this repository below root.
func (r Repo) Path(root string) string { return filepath.Join(root, r.Dir) }

// ServicePath is where one service of this repository sits in the worktree.
func (r Repo) ServicePath(root, service string) string {
	return filepath.Join(root, r.Dir, r.Rel[service])
}

// Changed reports whether any service of this repository is in set. A
// repository holds one checkout and one branch, so one changed service puts the
// whole worktree on the branch.
func (r Repo) Changed(set map[string]bool) bool {
	for _, s := range r.Services {
		if set[s] {
			return true
		}
	}
	return false
}

// Plan groups services by the repository that holds them and gives each
// repository one directory name.
//
// repoPathOf returns a service's directory in the user's checkout. taken holds
// directory names that are already in use below the destination root; Plan
// never hands one of them out. A repository takes the base name of its root, so
// a service that is the root of its own repository keeps the name it always
// had. Two repositories with the same base name both take a suffix of the hash
// of their root, so neither can silently overwrite the other.
func Plan(services []string, repoPathOf func(string) string, taken map[string]bool) ([]Repo, error) {
	names := append([]string(nil), services...)
	sort.Strings(names)

	byTop := map[string]*Repo{}
	var tops []string
	for _, s := range names {
		path := repoPathOf(s)
		if path == "" {
			return nil, fmt.Errorf("service %q has no repository path in the workspace manifest", s)
		}
		top, err := Toplevel(path)
		if err != nil {
			return nil, fmt.Errorf("service %q at %s is not in a git repository. devstack runs the services from git worktrees, so every service must be in a checkout", s, path)
		}
		rel, err := relativeTo(top, path)
		if err != nil {
			return nil, fmt.Errorf("service %q at %s: %w", s, path, err)
		}
		r, ok := byTop[top]
		if !ok {
			r = &Repo{Toplevel: top, Rel: map[string]string{}}
			byTop[top] = r
			tops = append(tops, top)
		}
		r.Services = append(r.Services, s)
		r.Rel[s] = rel
	}
	sort.Strings(tops)

	shared := map[string]int{}
	for _, top := range tops {
		shared[filepath.Base(top)]++
	}

	out := make([]Repo, 0, len(tops))
	for _, top := range tops {
		r := byTop[top]
		dir := filepath.Base(top)
		if shared[dir] > 1 || taken[dir] {
			dir = dir + "-" + shortHash(top)
		}
		if taken[dir] {
			return nil, fmt.Errorf("the directory %q below the destination root is already in use, so devstack can not put the worktree of %s there", dir, top)
		}
		r.Dir = dir
		out = append(out, *r)
	}
	return out, nil
}

// Toplevel is the root of the git repository that holds path, with symlinks
// resolved. A path below the root answers with the root, so it also reports the
// worktree root of a service that sits in a subdirectory.
func Toplevel(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel in %s failed: %w\n%s", path, err, strings.TrimSpace(string(out)))
	}
	return resolve(strings.TrimSpace(string(out))), nil
}

// IsRoot reports whether path is the root of a git repository, and names the
// repository root that holds it. A path that no repository holds answers false
// with an empty root.
//
// A caller that prints "commit here" needs both answers: a directory below a
// root commits the repository above it, and a directory in no repository
// commits nothing at all.
func IsRoot(path string) (bool, string) {
	top, err := Toplevel(path)
	if err != nil {
		return false, ""
	}
	return top == resolve(path), top
}

// CurrentBranch is the branch that the worktree at path has checked out. A
// detached worktree answers with an empty string.
func CurrentBranch(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = path
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD in %s failed: %w\n%s", path, err, strings.TrimSpace(string(out)))
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return "", nil
	}
	return branch, nil
}

// Attach puts the worktree at path on branch. If the repository does not have
// the branch, Attach creates it at the commit that the worktree has now, so a
// detached worktree keeps its commit and gets a branch to commit on.
func Attach(path, branch string) error {
	if branch == "" {
		return fmt.Errorf("devstack can not attach the worktree %s to an empty branch name", path)
	}
	exists, err := branchExists(path, branch)
	if err != nil {
		return err
	}
	args := []string{"checkout", branch}
	if !exists {
		args = []string{"checkout", "-b", branch}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s in %s failed: %w\n%s", strings.Join(args, " "), path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Head is the commit that the worktree at path has now. A caller that has to
// put a worktree back where it found it reads this first: a branch moves, and a
// commit does not.
func Head(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = path
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD in %s failed: %w\n%s", path, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Detach puts the worktree at path on commit, and on no branch. It is the
// reverse of Attach, for a caller that has to undo what it did.
func Detach(path, commit string) error {
	if commit == "" {
		return fmt.Errorf("devstack can not detach the worktree %s at an empty commit", path)
	}
	cmd := exec.Command("git", "checkout", "--detach", commit)
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout --detach %s in %s failed: %w\n%s", commit, path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func relativeTo(top, path string) (string, error) {
	rel, err := filepath.Rel(top, resolve(path))
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the path is outside its repository root %s", top)
	}
	return rel, nil
}

func resolve(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(path)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
