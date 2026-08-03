package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newRepo(t *testing.T) (root, repo string) {
	t.Helper()
	root = t.TempDir()
	repo = filepath.Join(root, "base")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.email", "t@example.com")
	git(t, repo, "config", "user.name", "tester")
	git(t, repo, "config", "commit.gpgsign", "false")
	write(t, filepath.Join(repo, "README.md"), "hello\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "init")
	return root, repo
}

func TestCreate_ChangedNewBranch(t *testing.T) {
	root, repo := newRepo(t)
	wt := filepath.Join(root, "base-feat")

	res, err := Create(repo, wt, "feature/x", "", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.SourceDirty {
		t.Errorf("SourceDirty = true, want false on clean base")
	}
	if got := git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/x" {
		t.Errorf("worktree branch = %q, want feature/x", got)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "README.md")); string(b) != "hello\n" {
		t.Errorf("worktree README = %q, want committed content", b)
	}
}

func TestCreate_ChangedBranchAlreadyExists(t *testing.T) {
	root, repo := newRepo(t)
	git(t, repo, "branch", "feature/x")
	wt := filepath.Join(root, "base-feat")

	res, err := Create(repo, wt, "feature/x", "", true)
	if err != nil {
		t.Fatalf("Create should attach to existing branch, got: %v", err)
	}
	if got := git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/x" {
		t.Errorf("worktree branch = %q, want feature/x", got)
	}
	_ = res
}

func TestCreate_DependentDetachedWhileBaseBranchLive(t *testing.T) {
	root, repo := newRepo(t)
	wt := filepath.Join(root, "base-dep")

	res, err := Create(repo, wt, "", "", false)
	if err != nil {
		t.Fatalf("dependent Create should succeed via detached HEAD, got: %v", err)
	}
	if got := git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("worktree HEAD = %q, want detached (HEAD)", got)
	}
	if got := git(t, wt, "rev-parse", "HEAD"); got != git(t, repo, "rev-parse", "HEAD") {
		t.Errorf("detached worktree not at base HEAD")
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "README.md")); string(b) != "hello\n" {
		t.Errorf("worktree README = %q, want committed content", b)
	}
	_ = res
}

// park moves the repo onto an unrelated branch with a commit of its own, so a
// worktree cut from the named ref is provably not cut from HEAD.
func park(t *testing.T, repo string) (parkedTip string) {
	t.Helper()
	git(t, repo, "checkout", "-q", "-b", "parked")
	write(t, filepath.Join(repo, "parked.txt"), "half-finished\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "parked work")
	return git(t, repo, "rev-parse", "HEAD")
}

func TestCreate_CutsFromNamedRefNotHead(t *testing.T) {
	root, repo := newRepo(t)
	mainTip := git(t, repo, "rev-parse", "HEAD")
	park(t, repo)

	feat := filepath.Join(root, "base-feat")
	res, err := Create(repo, feat, "feature/x", "refs/heads/main", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := git(t, feat, "rev-parse", "HEAD"); got != mainTip {
		t.Errorf("new branch cut at %s, want the named ref %s", got, mainTip)
	}
	if _, err := os.Stat(filepath.Join(feat, "parked.txt")); !os.IsNotExist(err) {
		t.Errorf("parked commit leaked into the worktree")
	}
	if !res.SourceOffRef {
		t.Errorf("SourceOffRef = false, want true when the checkout is parked elsewhere")
	}

	dep := filepath.Join(root, "base-dep")
	if _, err := Create(repo, dep, "", "refs/heads/main", false); err != nil {
		t.Fatalf("dependent Create: %v", err)
	}
	if got := git(t, dep, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("dependent worktree = %q, want detached", got)
	}
	if got := git(t, dep, "rev-parse", "HEAD"); got != mainTip {
		t.Errorf("dependent worktree detached at %s, want the named ref %s", got, mainTip)
	}
}

// An existing branch carries the history it already has: the cutting point
// applies to a branch being created, never to one being attached.
func TestCreate_ExistingBranchIgnoresFrom(t *testing.T) {
	root, repo := newRepo(t)
	mainTip := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "-b", "feature/x")
	write(t, filepath.Join(repo, "started.txt"), "earlier work\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "earlier work")
	featTip := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "main")

	wt := filepath.Join(root, "base-feat")
	if _, err := Create(repo, wt, "feature/x", "refs/heads/main", true); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := git(t, wt, "rev-parse", "HEAD"); got != featTip {
		t.Errorf("attached branch is at %s, want its own tip %s (mainTip is %s)", got, featTip, mainTip)
	}
}

func TestCreate_SourceOffRefFalseWhenHeadIsTheRef(t *testing.T) {
	root, repo := newRepo(t)
	res, err := Create(repo, filepath.Join(root, "base-feat"), "feature/x", "refs/heads/main", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.SourceOffRef {
		t.Errorf("SourceOffRef = true while the checkout sits on the ref itself")
	}
}

func TestCreate_DirtyBaseSucceedsAndSignals(t *testing.T) {
	root, repo := newRepo(t)
	write(t, filepath.Join(repo, "README.md"), "uncommitted change\n")
	wt := filepath.Join(root, "base-feat")

	res, err := Create(repo, wt, "feature/y", "", true)
	if err != nil {
		t.Fatalf("Create on dirty base should proceed, got: %v", err)
	}
	if !res.SourceDirty {
		t.Errorf("SourceDirty = false, want true on dirty base")
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "README.md")); string(b) != "hello\n" {
		t.Errorf("worktree README = %q, want committed content, not the dirty change", b)
	}
}

func TestRemove_CleanDirtyAndForce(t *testing.T) {
	root, repo := newRepo(t)

	clean := filepath.Join(root, "base-clean")
	if _, err := Create(repo, clean, "feat-clean", "", true); err != nil {
		t.Fatal(err)
	}
	if err := Remove(clean, false); err != nil {
		t.Fatalf("Remove clean worktree: %v", err)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Errorf("clean worktree still present after Remove")
	}

	dirty := filepath.Join(root, "base-dirty")
	if _, err := Create(repo, dirty, "feat-dirty", "", true); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dirty, "README.md"), "local edit\n")

	err := Remove(dirty, false)
	if err == nil {
		t.Fatalf("Remove of dirty worktree without force should error")
	}
	if !strings.Contains(err.Error(), dirty) {
		t.Errorf("error should name the path %q, got: %v", dirty, err)
	}
	if !strings.Contains(err.Error(), "refusing to remove without force") {
		t.Errorf("error should explain the refusal, got: %v", err)
	}
	if _, statErr := os.Stat(dirty); os.IsNotExist(statErr) {
		t.Errorf("dirty worktree was destroyed despite refusal")
	}

	if err := Remove(dirty, true); err != nil {
		t.Fatalf("Remove dirty worktree with force: %v", err)
	}
	if _, statErr := os.Stat(dirty); !os.IsNotExist(statErr) {
		t.Errorf("dirty worktree still present after forced Remove")
	}
}

func TestCreate_TwoWorktreesCoexist(t *testing.T) {
	root, repo := newRepo(t)
	wtA := filepath.Join(root, "base-a")
	wtB := filepath.Join(root, "base-b")

	if _, err := Create(repo, wtA, "feat-a", "", true); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	if _, err := Create(repo, wtB, "feat-b", "", true); err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if got := git(t, wtA, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-a" {
		t.Errorf("wtA branch = %q, want feat-a", got)
	}
	if got := git(t, wtB, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-b" {
		t.Errorf("wtB branch = %q, want feat-b", got)
	}
}

func TestMaterializeIgnoredConfig(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "base")
	if err := os.MkdirAll(filepath.Join(repo, "src", "API"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.email", "t@example.com")
	git(t, repo, "config", "user.name", "tester")
	git(t, repo, "config", "commit.gpgsign", "false")
	write(t, filepath.Join(repo, ".gitignore"), "appsettings.*.json\nobj/\n.envrc\ndevstack.service.yaml\n")
	write(t, filepath.Join(repo, "src", "API", "Program.cs"), "tracked\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "init")

	write(t, filepath.Join(repo, "src", "API", "appsettings.Development.json"), `{"conn":"secret"}`)
	write(t, filepath.Join(repo, ".envrc"), "export TOKEN=abc\n")
	write(t, filepath.Join(repo, "devstack.service.yaml"), "port: 5000\n")
	if err := os.MkdirAll(filepath.Join(repo, "obj"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "obj", "junk"), "build output\n")

	wt := filepath.Join(root, "base-feat")
	if _, err := Create(repo, wt, "feature/x", "", true); err != nil {
		t.Fatalf("Create: %v", err)
	}

	preexisting := filepath.Join(wt, "devstack.service.yaml")
	write(t, preexisting, "port: 9999\n")

	copied, err := MaterializeIgnoredConfig(repo, wt)
	if err != nil {
		t.Fatalf("MaterializeIgnoredConfig: %v", err)
	}

	appSettings := filepath.Join(wt, "src", "API", "appsettings.Development.json")
	if b, err := os.ReadFile(appSettings); err != nil {
		t.Errorf("appsettings.Development.json not materialized: %v", err)
	} else if string(b) != `{"conn":"secret"}` {
		t.Errorf("appsettings content = %q, want copied bytes", b)
	}

	if _, err := os.Stat(filepath.Join(wt, "obj", "junk")); !os.IsNotExist(err) {
		t.Errorf("obj/junk was copied into worktree; build output must be excluded")
	}

	if b, _ := os.ReadFile(preexisting); string(b) != "port: 9999\n" {
		t.Errorf("pre-existing worktree file was overwritten: %q", b)
	}

	want := []string{".envrc", "src/API/appsettings.Development.json"}
	if strings.Join(copied, "|") != strings.Join(want, "|") {
		t.Errorf("copied = %v, want %v", copied, want)
	}
}

func TestPrune_ReclaimsHandDeletedWorktree(t *testing.T) {
	root, repo := newRepo(t)
	wt := filepath.Join(root, "base-gone")
	if _, err := Create(repo, wt, "feat-gone", "", true); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	before := git(t, repo, "worktree", "list")
	if !strings.Contains(before, "base-gone") {
		t.Fatalf("expected stale worktree still listed before prune:\n%s", before)
	}
	if err := Prune(repo); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	after := git(t, repo, "worktree", "list")
	if strings.Contains(after, "base-gone") {
		t.Errorf("stale worktree still listed after prune:\n%s", after)
	}
}
