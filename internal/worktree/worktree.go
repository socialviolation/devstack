package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
)

type Result struct {
	SourceDirty  bool
	SourceOffRef bool
}

// An existing branch is attached, keeping the history it already has, so from
// does not apply to it. A dependent repo (changed == false) detaches instead:
// git refuses to check out a branch a second time while it is live in another
// working tree.
func Create(repoPath, worktreePath, branch, from string, changed bool) (Result, error) {
	dirty, err := HasUncommittedChanges(repoPath)
	if err != nil {
		return Result{}, err
	}
	res := Result{SourceDirty: dirty}
	if from != "" {
		offRef, err := headOffRef(repoPath, from)
		if err != nil {
			return res, err
		}
		res.SourceOffRef = offRef
	}

	cutFrom := from
	var args []string
	switch {
	case !changed:
		args = []string{"worktree", "add", "--detach", worktreePath}
	case branch == "":
		return res, fmt.Errorf("the changed repo %s needs a branch name", repoPath)
	default:
		exists, err := branchExists(repoPath, branch)
		if err != nil {
			return res, err
		}
		if exists {
			args = []string{"worktree", "add", worktreePath, branch}
			cutFrom = ""
		} else {
			args = []string{"worktree", "add", "-b", branch, worktreePath}
		}
	}
	if cutFrom != "" {
		args = append(args, cutFrom)
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return res, fmt.Errorf("git worktree add %s failed: %w\n%s", worktreePath, err, strings.TrimSpace(string(out)))
	}
	return res, nil
}

func Remove(worktreePath string, force bool) error {
	if !force {
		dirty, err := HasUncommittedChanges(worktreePath)
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("the worktree %s has uncommitted changes. devstack can not remove it without force", worktreePath)
		}
	}

	out, err := runRemove(worktreePath, force)
	if err != nil && !force {
		// git refuses to remove a working tree that holds a populated
		// submodule, however clean that tree is. The check above proves the
		// tree is clean, so --force here discards no work.
		out, err = runRemove(worktreePath, true)
	}
	if err != nil {
		return fmt.Errorf("git worktree remove %s failed: %w\n%s", worktreePath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runRemove(worktreePath string, force bool) ([]byte, error) {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)
	cmd := exec.Command("git", args...)
	cmd.Dir = worktreePath
	return cmd.CombinedOutput()
}

func Prune(repoPath string) error {
	cmd := exec.Command("git", "worktree", "prune")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree prune failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func HasUncommittedChanges(path string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = path
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status in %s failed: %w\n%s", path, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

var configPatterns = []string{
	"appsettings*.json",
	"*.local.json",
	"*.local.js",
	".envrc",
	".env",
	".env.*",
	"devstack.*.yaml",
}

func isConfigFile(rel string) bool {
	base := filepath.Base(rel)
	// A worktree runs from the generated manifest of its stack. Copying the
	// template's over it would point the stack back at base's repositories.
	if base == config.WorkspaceManifestFileName {
		return false
	}
	for _, p := range configPatterns {
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}

// MaterializeIgnoredConfig copies a base repo's machine-local, git-ignored config
// files into a freshly created worktree. A git worktree checks out committed state
// only, so gitignored dev config (appsettings.Development.json, .envrc,
// devstack.service.yaml) is absent and the service boots without it. It copies only
// files whose basename matches configPatterns — never build output (bin/, obj/,
// node_modules/) that git also ignores. It never overwrites a file the worktree
// already holds, and never reads or logs file contents (they carry secrets); it
// copies bytes and returns the relative paths materialized.
func MaterializeIgnoredConfig(baseRepo, worktreePath string) ([]string, error) {
	return materializeIgnoredConfig(baseRepo, worktreePath, false)
}

func MaterializeIgnoredConfigForce(baseRepo, worktreePath string) ([]string, error) {
	return materializeIgnoredConfig(baseRepo, worktreePath, true)
}

func materializeIgnoredConfig(baseRepo, worktreePath string, overwrite bool) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--others", "--ignored", "--exclude-standard")
	cmd.Dir = baseRepo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git ls-files ignored in %s failed: %w\n%s", baseRepo, err, strings.TrimSpace(string(out)))
	}

	var copied []string
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		rel = strings.TrimSpace(rel)
		if rel == "" || strings.HasSuffix(rel, "/") || !isConfigFile(rel) {
			continue
		}
		src := filepath.Join(baseRepo, rel)
		dst := filepath.Join(worktreePath, rel)
		if _, err := os.Stat(dst); err == nil && !overwrite {
			continue
		}
		if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := copyFile(src, dst, overwrite); err != nil {
			return copied, fmt.Errorf("can not materialize %s into the worktree: %w", rel, err)
		}
		copied = append(copied, rel)
	}
	sort.Strings(copied)
	return copied, nil
}

func copyFile(src, dst string, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	out, err := os.OpenFile(dst, flags, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func headOffRef(repoPath, ref string) (bool, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD", ref)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git rev-parse %s in %s failed: %w\n%s", ref, repoPath, err, strings.TrimSpace(string(out)))
	}
	lines := strings.Fields(string(out))
	if len(lines) != 2 {
		return false, fmt.Errorf("git rev-parse %s in %s returned %d revisions, and devstack needs 2", ref, repoPath, len(lines))
	}
	return lines[0] != lines[1], nil
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
