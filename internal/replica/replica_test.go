package replica

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

const backendSvc = `version: 1
service:
  name: backend
runtime:
  run:
    command: go run .
ports:
  http: 8080
`

const frontendSvc = `version: 1
service:
  name: frontend
runtime:
  run:
    command: npm run dev
`

const workerSvc = `version: 1
service:
  name: worker
runtime:
  run:
    command: go run ./worker
`

func rungit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func makeRepo(t *testing.T, dir, manifest string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "devstack.service.yaml"), manifest)
	rungit(t, dir, "init", "-b", "main", "-q")
	rungit(t, dir, "config", "commit.gpgsign", "false")
	rungit(t, dir, "add", "-f", ".")
	rungit(t, dir, "commit", "-q", "-m", "init")
}

func writeWorkspaceManifest(t *testing.T, root string, repos ...string) {
	t.Helper()
	manifest := `version: 1
workspace:
  name: navexa
  env: dev
  repoDiscovery:
    mode: explicit
    repos:
`
	for _, r := range repos {
		manifest += "      - ./" + r + "\n"
	}
	manifest += `environments:
  dev:
    values:
      TIER: dev
  perf:
    values:
      TIER: perf
`
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), manifest)
}

func newTemplate(t *testing.T) *workspace.Workspace {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	makeRepo(t, filepath.Join(root, "backend"), backendSvc)
	makeRepo(t, filepath.Join(root, "frontend"), frontendSvc)
	writeWorkspaceManifest(t, root, "backend", "frontend")
	return &workspace.Workspace{Name: "navexa", Path: root}
}

func newClonedTemplate(t *testing.T) (ws *workspace.Workspace, upstream, origin string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	upstream = filepath.Join(home, "upstream", "backend")
	origin = filepath.Join(home, "origin", "backend.git")
	makeRepo(t, upstream, backendSvc)
	rungit(t, home, "clone", "--quiet", "--bare", upstream, origin)
	rungit(t, home, "clone", "--quiet", origin, filepath.Join(root, "backend"))
	makeRepo(t, filepath.Join(root, "frontend"), frontendSvc)
	writeWorkspaceManifest(t, root, "backend", "frontend")
	return &workspace.Workspace{Name: "navexa", Path: root}, upstream, origin
}

func services(wts []Worktree) []string {
	var names []string
	for _, wt := range wts {
		names = append(names, wt.Service)
	}
	return names
}

func TestEnsureCreatesDetachedWorktrees(t *testing.T) {
	ws := newTemplate(t)

	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if got := strings.Join(services(res.Created), ","); got != "backend,frontend" {
		t.Errorf("Created = %v, want backend,frontend", services(res.Created))
	}
	if len(res.Existing) != 0 {
		t.Errorf("Existing = %v, want none on first Ensure", services(res.Existing))
	}
	if res.Root != filepath.Join(filepath.Dir(ws.Path), ".devstack-base", "navexa") {
		t.Errorf("Root = %q, want a .devstack-base sibling of the checkout", res.Root)
	}

	for _, name := range []string{"backend", "frontend"} {
		wt := filepath.Join(res.Root, name)
		if head := rungit(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); head != "HEAD" {
			t.Errorf("%s worktree HEAD = %q, want detached", name, head)
		}
		want := rungit(t, filepath.Join(ws.Path, name), "rev-parse", "main")
		if got := rungit(t, wt, "rev-parse", "HEAD"); got != want {
			t.Errorf("%s worktree at %s, want default branch tip %s", name, got, want)
		}
	}
	for _, wt := range res.Created {
		if wt.Branch != "main" {
			t.Errorf("%s Branch = %q, want main", wt.Service, wt.Branch)
		}
	}

	rw, err := config.ResolveWorkspace(res.Root)
	if err != nil {
		t.Fatalf("resolve generated manifest: %v", err)
	}
	if rw.Manifest.Workspace.Name != "navexa" {
		t.Errorf("replica workspace name = %q, want navexa unchanged", rw.Manifest.Workspace.Name)
	}
	if len(rw.Services) != 2 {
		t.Fatalf("replica services = %v, want both services in the overlay", rw.Services)
	}
	if got := rw.Services["backend"].RepoPath; got != filepath.Join(res.Root, "backend") {
		t.Errorf("backend RepoPath = %q, want the replica worktree", got)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	ws := newTemplate(t)
	first, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	before := rungit(t, filepath.Join(first.Root, "backend"), "rev-parse", "HEAD")

	second, err := Ensure(ws)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if len(second.Created) != 0 {
		t.Errorf("Created = %v on second Ensure, want none", services(second.Created))
	}
	if got := strings.Join(services(second.Existing), ","); got != "backend,frontend" {
		t.Errorf("Existing = %v, want backend,frontend", services(second.Existing))
	}
	if len(second.Removed) != 0 || len(second.Warnings) != 0 {
		t.Errorf("second Ensure removed %v / warned %v, want neither", second.Removed, second.Warnings)
	}
	if after := rungit(t, filepath.Join(first.Root, "backend"), "rev-parse", "HEAD"); after != before {
		t.Errorf("backend worktree moved on a no-op Ensure: %s -> %s", before, after)
	}
}

func TestEnsureAddsServiceAddedToManifest(t *testing.T) {
	ws := newTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	makeRepo(t, filepath.Join(ws.Path, "worker"), workerSvc)
	writeWorkspaceManifest(t, ws.Path, "backend", "frontend", "worker")

	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure after adding a service: %v", err)
	}
	if got := strings.Join(services(res.Created), ","); got != "worker" {
		t.Errorf("Created = %v, want only worker", services(res.Created))
	}
	if _, err := os.Stat(filepath.Join(res.Root, "worker")); err != nil {
		t.Errorf("worker worktree not created: %v", err)
	}
}

func TestEnsureRemovesServiceDroppedFromManifest(t *testing.T) {
	ws := newTemplate(t)
	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	writeWorkspaceManifest(t, ws.Path, "backend")

	res, err = Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure after dropping a service: %v", err)
	}
	if strings.Join(res.Removed, ",") != "frontend" {
		t.Errorf("Removed = %v, want frontend", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(res.Root, "frontend")); !os.IsNotExist(err) {
		t.Errorf("frontend worktree still present after it left the manifest")
	}

	rw, err := config.ResolveWorkspace(res.Root)
	if err != nil {
		t.Fatalf("resolve generated manifest: %v", err)
	}
	if _, ok := rw.Services["frontend"]; ok {
		t.Errorf("replica manifest still lists frontend: %v", rw.Services)
	}
}

// The whole point of the replica: the user's checkout is a template, so neither
// Ensure nor Sync may move its branch or touch work parked in it.
func TestTemplateCheckoutLeftUntouched(t *testing.T) {
	ws := newTemplate(t)
	backend := filepath.Join(ws.Path, "backend")
	mainTip := rungit(t, backend, "rev-parse", "main")
	rungit(t, backend, "checkout", "-q", "-b", "wip")
	writeFile(t, filepath.Join(backend, "committed-wip.txt"), "committed on wip\n")
	rungit(t, backend, "add", "-f", "committed-wip.txt")
	rungit(t, backend, "commit", "-q", "-m", "wip")
	writeFile(t, filepath.Join(backend, "devstack.service.yaml"), backendSvc+"# half-finished\n")
	writeFile(t, filepath.Join(backend, "scratch.txt"), "wip\n")

	branchBefore := rungit(t, backend, "rev-parse", "--abbrev-ref", "HEAD")
	statusBefore := rungit(t, backend, "status", "--porcelain")

	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wt := filepath.Join(res.Root, "backend")
	if got := rungit(t, wt, "rev-parse", "HEAD"); got != mainTip {
		t.Errorf("Ensure left the worktree at %s, want the default branch tip %s, not the template's parked HEAD", got, mainTip)
	}
	if _, err := os.Stat(filepath.Join(wt, "committed-wip.txt")); !os.IsNotExist(err) {
		t.Errorf("the template's parked branch leaked into the replica")
	}

	if _, err := Sync(ws); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := rungit(t, wt, "rev-parse", "HEAD"); got != mainTip {
		t.Errorf("Sync left the worktree at %s, want the default branch tip %s", got, mainTip)
	}

	if got := rungit(t, backend, "rev-parse", "--abbrev-ref", "HEAD"); got != branchBefore {
		t.Errorf("template branch = %q, want %q", got, branchBefore)
	}
	if got := rungit(t, backend, "status", "--porcelain"); got != statusBefore {
		t.Errorf("template working tree changed:\nbefore:\n%s\nafter:\n%s", statusBefore, got)
	}
	if got := readFile(t, filepath.Join(backend, "scratch.txt")); got != "wip\n" {
		t.Errorf("template scratch.txt = %q, want untouched", got)
	}
}

func TestSyncAdvancesWorktreeToDefaultBranchTip(t *testing.T) {
	ws := newTemplate(t)
	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wt := filepath.Join(res.Root, "backend")
	before := rungit(t, wt, "rev-parse", "--short", "HEAD")

	backend := filepath.Join(ws.Path, "backend")
	writeFile(t, filepath.Join(backend, "new.txt"), "moved on\n")
	rungit(t, backend, "add", "-f", "new.txt")
	rungit(t, backend, "commit", "-q", "-m", "advance main")
	tip := rungit(t, backend, "rev-parse", "--short", "main")

	sync, err := Sync(ws)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	var backendSync ServiceSync
	for _, s := range sync.Services {
		if s.Service == "backend" {
			backendSync = s
		}
	}
	if backendSync.Before != before || backendSync.After != tip {
		t.Errorf("backend sync = %+v, want before %s after %s", backendSync, before, tip)
	}
	if backendSync.Ref != "main" {
		t.Errorf("backend Ref = %q, want main", backendSync.Ref)
	}
	if got := rungit(t, wt, "rev-parse", "--short", "HEAD"); got != tip {
		t.Errorf("worktree HEAD = %s, want the new tip %s", got, tip)
	}
	if got := rungit(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("worktree HEAD = %q after sync, want still detached", got)
	}
	if _, err := os.Stat(filepath.Join(wt, "new.txt")); err != nil {
		t.Errorf("new commit's file missing from the worktree: %v", err)
	}
}

// A service with a remote tracks origin's default branch, not the local branch
// the user last pulled: the replica is meant to run what the team shipped.
func TestSyncFollowsOriginNotTheStaleLocalBranch(t *testing.T) {
	ws, upstream, origin := newClonedTemplate(t)

	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	rungit(t, upstream, "remote", "add", "origin", origin)
	writeFile(t, filepath.Join(upstream, "shipped.txt"), "from the team\n")
	rungit(t, upstream, "add", "-f", "shipped.txt")
	rungit(t, upstream, "commit", "-q", "-m", "ship")
	rungit(t, upstream, "push", "--quiet", "origin", "main")
	shipped := rungit(t, upstream, "rev-parse", "--short", "main")

	sync, err := Sync(ws)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sync.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none when the fetch succeeds", sync.Warnings)
	}

	var backendSync ServiceSync
	for _, s := range sync.Services {
		if s.Service == "backend" {
			backendSync = s
		}
	}
	if backendSync.Ref != "origin/main" {
		t.Errorf("backend Ref = %q, want origin/main", backendSync.Ref)
	}
	if backendSync.After != shipped {
		t.Errorf("backend synced to %s, want the pushed tip %s", backendSync.After, shipped)
	}

	template := filepath.Join(ws.Path, "backend")
	if got := rungit(t, template, "rev-parse", "--short", "main"); got == shipped {
		t.Fatalf("the template checkout was fast-forwarded; the test no longer proves origin is what is followed")
	}
	if _, err := os.Stat(filepath.Join(res.Root, "backend", "shipped.txt")); err != nil {
		t.Errorf("pushed commit's file missing from the worktree: %v", err)
	}
}

// Being offline must not stop the replica from running: an unreachable origin is
// a warning, and the worktree still syncs to the ref it already has.
func TestSyncUnreachableOriginWarnsAndKeepsGoing(t *testing.T) {
	ws, _, origin := newClonedTemplate(t)
	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := os.RemoveAll(origin); err != nil {
		t.Fatal(err)
	}
	want := rungit(t, filepath.Join(res.Root, "backend"), "rev-parse", "--short", "HEAD")

	sync, err := Sync(ws)
	if err != nil {
		t.Fatalf("Sync with an unreachable origin should warn, not fail: %v", err)
	}
	if len(sync.Warnings) != 1 || !strings.Contains(sync.Warnings[0], "backend") {
		t.Errorf("Warnings = %v, want one naming backend", sync.Warnings)
	}
	var backendSync ServiceSync
	for _, s := range sync.Services {
		if s.Service == "backend" {
			backendSync = s
		}
	}
	if backendSync.After != want {
		t.Errorf("backend synced to %s, want the last known tip %s", backendSync.After, want)
	}
}

// The replica is nobody's working copy, so a config file that drifted from the
// template must be replaced rather than kept.
func TestSyncOverwritesStaleMaterializedConfig(t *testing.T) {
	ws := newTemplate(t)
	backend := filepath.Join(ws.Path, "backend")
	writeFile(t, filepath.Join(backend, ".gitignore"), ".envrc\n")
	rungit(t, backend, "add", "-f", ".gitignore")
	rungit(t, backend, "commit", "-q", "-m", "gitignore")
	writeFile(t, filepath.Join(backend, ".envrc"), "export TOKEN=v1\n")

	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	copied := filepath.Join(res.Root, "backend", ".envrc")
	if got := readFile(t, copied); got != "export TOKEN=v1\n" {
		t.Fatalf("materialized .envrc = %q, want the template's bytes", got)
	}

	writeFile(t, filepath.Join(backend, ".envrc"), "export TOKEN=v2\n")
	writeFile(t, copied, "export TOKEN=stale\n")

	if _, err := Sync(ws); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := readFile(t, copied); got != "export TOKEN=v2\n" {
		t.Errorf("worktree .envrc = %q, want the template's current bytes", got)
	}
}

func TestResolveBeforeEnsure(t *testing.T) {
	ws := newTemplate(t)

	_, err := Resolve(ws)
	if err == nil {
		t.Fatal("Resolve = nil error before the replica exists")
	}
	if !strings.Contains(err.Error(), "devstack workspace up") {
		t.Errorf("error should say what to run next, got: %v", err)
	}
	if _, err := Sync(ws); err == nil {
		t.Fatal("Sync = nil error before the replica exists")
	}
}

func TestResolveInheritsTemplateEnvironments(t *testing.T) {
	ws := newTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	rw, err := Resolve(ws)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := rw.Manifest.Environments["perf"]; !ok {
		t.Errorf("Environments = %v, want the template's definitions folded in", rw.Manifest.Environments)
	}
	if rw.Manifest.Workspace.Env != "dev" {
		t.Errorf("Workspace.Env = %q, want the template's selection dev", rw.Manifest.Workspace.Env)
	}
	if got := rw.Services["backend"].RepoPath; got != filepath.Join(Root(ws), "backend") {
		t.Errorf("backend RepoPath = %q, want the replica worktree", got)
	}
}

func TestEnsureServiceDirIsNotAGitRepo(t *testing.T) {
	ws := newTemplate(t)
	writeFile(t, filepath.Join(ws.Path, "worker", "devstack.service.yaml"), workerSvc)
	writeWorkspaceManifest(t, ws.Path, "backend", "frontend", "worker")

	_, err := Ensure(ws)
	if err == nil {
		t.Fatal("Ensure = nil error for a service directory that is not a git repo")
	}
	if !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), filepath.Join(ws.Path, "worker")) {
		t.Errorf("error should name the service and its path, got: %v", err)
	}
}
