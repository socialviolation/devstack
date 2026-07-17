// Package worktree manages the git worktree lifecycle for feature stacks: one
// worktree per overlay service, always created as a sibling of the base checkout.
package worktree

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Result reports facts about a Create that the CLI layer needs but a library
// must not print itself.
type Result struct {
	// SourceDirty is true when the source repo had uncommitted changes at create
	// time. The worktree still gets committed (HEAD) state; the caller decides
	// whether to warn that the working-tree changes were left behind.
	SourceDirty bool
}

// Create adds a worktree at worktreePath for the repo at repoPath.
//
// A changed repo gets a feature branch: a new branch named branch off the repo's
// current HEAD, or, when branch already exists, an attach (checkout) of it. A
// dependent repo (changed == false) is unmodified code isolated only for config,
// so it gets a detached HEAD at current HEAD — this avoids git's refusal to check
// out the base's branch a second time while it is live in the main working tree.
func Create(repoPath, worktreePath, branch string, changed bool) (Result, error) {
	dirty, err := HasUncommittedChanges(repoPath)
	if err != nil {
		return Result{}, err
	}
	res := Result{SourceDirty: dirty}

	var args []string
	switch {
	case !changed:
		args = []string{"worktree", "add", "--detach", worktreePath}
	case branch == "":
		return res, fmt.Errorf("changed repo %s requires a branch name", repoPath)
	default:
		exists, err := branchExists(repoPath, branch)
		if err != nil {
			return res, err
		}
		if exists {
			args = []string{"worktree", "add", worktreePath, branch}
		} else {
			args = []string{"worktree", "add", "-b", branch, worktreePath}
		}
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return res, fmt.Errorf("git worktree add %s failed: %w\n%s", worktreePath, err, strings.TrimSpace(string(out)))
	}
	return res, nil
}

// Remove deletes the worktree at worktreePath. It refuses a worktree with
// uncommitted changes unless force is set, so uncommitted work is never destroyed
// silently. A clean Remove also cleans git's admin dir; no separate prune needed.
func Remove(worktreePath string, force bool) error {
	if !force {
		dirty, err := HasUncommittedChanges(worktreePath)
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("worktree %s has uncommitted changes; refusing to remove without force", worktreePath)
		}
	}

	args := []string{"worktree", "remove", worktreePath}
	if force {
		args = []string{"worktree", "remove", "--force", worktreePath}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = worktreePath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove %s failed: %w\n%s", worktreePath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Prune removes admin records for worktrees whose directories were deleted by
// hand. A clean Remove does not need this; reconciliation of stray worktrees does.
func Prune(repoPath string) error {
	cmd := exec.Command("git", "worktree", "prune")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree prune failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HasUncommittedChanges reports whether the working tree at path has staged,
// unstaged, or untracked changes.
func HasUncommittedChanges(path string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = path
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status in %s failed: %w\n%s", path, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func branchExists(repoPath, branch string) (bool, error) {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repoPath
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git show-ref for branch %s failed: %w", branch, err)
}
