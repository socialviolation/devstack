package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func noDaemon(t *testing.T) {
	t.Helper()
	orig := daemonReachable
	daemonReachable = func(int) bool { return false }
	t.Cleanup(func() { daemonReachable = orig })
}

func park(t *testing.T, repo string) string {
	t.Helper()
	git(t, repo, "checkout", "-q", "-b", "parked")
	writeFile(t, filepath.Join(repo, "parked.txt"), "half-finished\n")
	git(t, repo, "add", "-f", ".")
	git(t, repo, "commit", "-q", "-m", "parked work")
	return gitOut(t, repo, "rev-parse", "HEAD")
}

func worktreesByService(res *CreateResult) map[string]WorktreeResult {
	out := map[string]WorktreeResult{}
	for _, w := range res.Worktrees {
		out[w.Service] = w
	}
	return out
}

// The checkout is a template a user parks work in, so its HEAD says nothing
// about where a new stack starts. Both the changed repo's branch and the
// dependent repo's detached HEAD must land on the default branch.
func TestCreateCutsFromDefaultBranchNotTheParkedCheckout(t *testing.T) {
	base := newBase(t)
	noDaemon(t)

	backend := filepath.Join(base.Path, "backend")
	frontend := filepath.Join(base.Path, "frontend")
	backendDefault := gitOut(t, backend, "rev-parse", "HEAD")
	frontendDefault := gitOut(t, frontend, "rev-parse", "HEAD")
	park(t, backend)
	park(t, frontend)

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wt := worktreesByService(res)

	if got := gitOut(t, wt["backend"].Path, "rev-parse", "HEAD"); got != backendDefault {
		t.Errorf("backend worktree at %s, want the default branch tip %s", got, backendDefault)
	}
	if got := gitOut(t, wt["backend"].Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat" {
		t.Errorf("backend worktree branch = %q, want feat", got)
	}
	if _, err := os.Stat(filepath.Join(wt["backend"].Path, "parked.txt")); !os.IsNotExist(err) {
		t.Errorf("the parked branch's commit landed in the stack")
	}

	if got := gitOut(t, wt["frontend"].Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("frontend worktree = %q, want detached", got)
	}
	if got := gitOut(t, wt["frontend"].Path, "rev-parse", "HEAD"); got != frontendDefault {
		t.Errorf("frontend worktree detached at %s, want the default branch tip %s", got, frontendDefault)
	}

	// No origin on these repos: the local default branch is the only candidate.
	if wt["backend"].Ref != "main" {
		t.Errorf("backend cut ref = %q, want main from the local fallback", wt["backend"].Ref)
	}

	var warning string
	for _, w := range res.Warnings {
		if strings.Contains(w, "cut from") {
			warning = w
		}
	}
	if warning == "" {
		t.Fatalf("no warning that the checkout's own work was left behind: %v", res.Warnings)
	}
	if !strings.Contains(warning, "main (backend)") || !strings.Contains(warning, "main (frontend)") {
		t.Errorf("warning should name the ref each service was cut from, got: %s", warning)
	}
}

// A clean checkout sitting on the default branch has nothing left behind, so it
// must not be warned about.
func TestCreateWarnsOnlyWhenTheCheckoutHoldsMore(t *testing.T) {
	base := newBase(t)
	noDaemon(t)

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "cut from") {
			t.Errorf("warned about left-behind work on a checkout that holds none: %s", w)
		}
	}
}

func TestCreateFromOverridesTheDefaultBranch(t *testing.T) {
	base := newBase(t)
	noDaemon(t)

	backend := filepath.Join(base.Path, "backend")
	parkedTip := park(t, backend)
	git(t, backend, "checkout", "-q", "main")
	park(t, filepath.Join(base.Path, "frontend"))

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}, From: "refs/heads/parked"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wt := worktreesByService(res)

	if got := gitOut(t, wt["backend"].Path, "rev-parse", "HEAD"); got != parkedTip {
		t.Errorf("backend worktree at %s, want the --from ref %s", got, parkedTip)
	}
	if _, err := os.Stat(filepath.Join(wt["backend"].Path, "parked.txt")); err != nil {
		t.Errorf("--from ref's commit missing from the worktree: %v", err)
	}
	if got := gitOut(t, wt["frontend"].Path, "rev-parse", "HEAD"); got != gitOut(t, filepath.Join(base.Path, "frontend"), "rev-parse", "refs/heads/parked") {
		t.Errorf("dependent worktree did not honour --from")
	}
}

// Resuming a stack must not rewrite where its branch started.
func TestCreateAttachesExistingBranchInsteadOfRecutting(t *testing.T) {
	base := newBase(t)
	noDaemon(t)

	backend := filepath.Join(base.Path, "backend")
	writeFile(t, filepath.Join(backend, "earlier.txt"), "yesterday\n")
	git(t, backend, "checkout", "-q", "-b", "feat")
	git(t, backend, "add", "-f", ".")
	git(t, backend, "commit", "-q", "-m", "earlier work")
	featTip := gitOut(t, backend, "rev-parse", "HEAD")
	git(t, backend, "checkout", "-q", "main")

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wt := worktreesByService(res)

	if got := gitOut(t, wt["backend"].Path, "rev-parse", "HEAD"); got != featTip {
		t.Errorf("existing branch re-cut at %s, want its own tip %s", got, featTip)
	}
	if _, err := os.Stat(filepath.Join(wt["backend"].Path, "earlier.txt")); err != nil {
		t.Errorf("attaching the existing branch lost its commits: %v", err)
	}
}

// A local default branch that was never pulled is stale, so origin's copy is the
// one a stack starts from.
func TestCreateCutsFromOriginNotTheStaleLocalBranch(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	noDaemon(t)

	upstream := filepath.Join(tmpHome, "upstream")
	makeRepo(t, upstream, backendSvc)
	writeFile(t, filepath.Join(upstream, "shipped.txt"), "on origin\n")
	git(t, upstream, "add", "-f", ".")
	git(t, upstream, "commit", "-q", "-m", "shipped")

	origin := filepath.Join(tmpHome, "origin.git")
	git(t, tmpHome, "clone", "-q", "--bare", upstream, origin)

	baseRoot := filepath.Join(tmpHome, "dev", "navexa")
	if err := os.MkdirAll(baseRoot, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	git(t, baseRoot, "clone", "-q", origin, "backend")
	backend := filepath.Join(baseRoot, "backend")
	originTip := gitOut(t, backend, "rev-parse", "refs/remotes/origin/main")
	git(t, backend, "reset", "--hard", "-q", "HEAD~1")
	if gitOut(t, backend, "rev-parse", "refs/heads/main") == originTip {
		t.Fatal("local main was not made stale")
	}
	makeRepo(t, filepath.Join(baseRoot, "frontend"), frontendSvc)

	writeFile(t, filepath.Join(baseRoot, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./frontend
calls:
  frontend:
    - backend
`)
	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: baseRoot, TiltPort: 10350}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wt := worktreesByService(res)

	if got := gitOut(t, wt["backend"].Path, "rev-parse", "HEAD"); got != originTip {
		t.Errorf("backend worktree at %s, want origin/main %s", got, originTip)
	}
	if wt["backend"].Ref != "origin/main" {
		t.Errorf("backend cut ref = %q, want origin/main", wt["backend"].Ref)
	}
	if _, err := os.Stat(filepath.Join(wt["backend"].Path, "shipped.txt")); err != nil {
		t.Errorf("origin's tip missing from the worktree: %v", err)
	}
	// The frontend has no origin at all, so it must fall back to its local branch.
	if wt["frontend"].Ref != "main" {
		t.Errorf("frontend cut ref = %q, want the local main fallback", wt["frontend"].Ref)
	}
}
