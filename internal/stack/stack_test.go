package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
env:
  values:
    PORT: "${self.port.http}"
`

const frontendSvc = `version: 1
service:
  name: frontend
runtime:
  run:
    command: npm run dev
env:
  values:
    BACKEND_URL: "${backend.url}"
`

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
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

func makeRepo(t *testing.T, dir, manifest string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "devstack.service.yaml"), manifest)
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-f", ".")
	git(t, dir, "commit", "-q", "-m", "init")
}

func newBase(t *testing.T) *workspace.Workspace {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot := filepath.Join(tmpHome, "dev", "navexa")
	makeRepo(t, filepath.Join(baseRoot, "backend"), backendSvc)
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
	return base
}

func TestCreateOverlayWorktreesAndPorts(t *testing.T) {
	base := newBase(t)

	orig := daemonReachable
	daemonReachable = func(int) bool { return false }
	defer func() { daemonReachable = orig }()

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if res.StackName != "navexa--feat" {
		t.Errorf("StackName = %q, want navexa--feat", res.StackName)
	}

	reason := map[string]string{}
	for _, m := range res.Overlay {
		reason[m.Service] = m.Reason
	}
	if reason["backend"] != "changed" {
		t.Errorf("backend reason = %q, want changed", reason["backend"])
	}
	if reason["frontend"] != "caller" {
		t.Errorf("frontend reason = %q, want caller (transitive dependent)", reason["frontend"])
	}

	wt := map[string]WorktreeResult{}
	for _, w := range res.Worktrees {
		wt[w.Service] = w
		if _, err := os.Stat(w.Path); err != nil {
			t.Errorf("worktree path %s not created: %v", w.Path, err)
		}
	}
	if wt["backend"].Branch != "feat" || wt["backend"].Detached {
		t.Errorf("backend worktree = %+v, want branch feat", wt["backend"])
	}
	if !wt["frontend"].Detached || wt["frontend"].Branch != "" {
		t.Errorf("frontend worktree = %+v, want detached", wt["frontend"])
	}

	port := res.Ports[QualifyPortKey("backend", "http")]
	if port == 0 {
		t.Errorf("no port allocated for backend/http: %v", res.Ports)
	}
	if _, ok := res.Ports[QualifyPortKey("frontend", "http")]; ok {
		t.Errorf("frontend has no ports declared, should not be allocated one: %v", res.Ports)
	}

	// Base daemon is not running in the test, so a warning must be surfaced as data.
	foundBaseWarning := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "not reachable") {
			foundBaseWarning = true
		}
	}
	if !foundBaseWarning {
		t.Errorf("expected a base-not-running warning in result data, got %v", res.Warnings)
	}

	stacks, err := List(base.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stacks) != 1 || stacks[0].Name != "navexa--feat" {
		t.Fatalf("List = %+v, want one stack navexa--feat", stacks)
	}
	if stacks[0].BaseName != "navexa" {
		t.Errorf("stack base = %q, want navexa", stacks[0].BaseName)
	}

	rmRes, err := Remove(base, "feat", false)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !rmRes.Deregistered || !rmRes.RootRemoved {
		t.Errorf("Remove result = %+v, want deregistered + root removed", rmRes)
	}
	if len(rmRes.RemovedWorktrees) != 2 {
		t.Errorf("removed %d worktrees, want 2", len(rmRes.RemovedWorktrees))
	}
	if _, err := os.Stat(res.StackRoot); !os.IsNotExist(err) {
		t.Errorf("stack root %s not removed", res.StackRoot)
	}

	stacks, err = List(base.Name)
	if err != nil {
		t.Fatalf("List after rm: %v", err)
	}
	if len(stacks) != 0 {
		t.Errorf("List after rm = %+v, want empty", stacks)
	}
}

func TestCreateMaterializesIgnoredConfig(t *testing.T) {
	base := newBase(t)

	backend := filepath.Join(base.Path, "backend")
	writeFile(t, filepath.Join(backend, ".gitignore"), "appsettings.*.json\nobj/\n")
	git(t, backend, "add", "-f", ".gitignore")
	git(t, backend, "commit", "-q", "-m", "gitignore")
	writeFile(t, filepath.Join(backend, "appsettings.Development.json"), `{"ConnectionStrings":{"Db":"secret"}}`)
	writeFile(t, filepath.Join(backend, "obj", "junk.dll"), "build output")

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var backendWT WorktreeResult
	for _, w := range res.Worktrees {
		if w.Service == "backend" {
			backendWT = w
		}
	}

	dst := filepath.Join(backendWT.Path, "appsettings.Development.json")
	if b, err := os.ReadFile(dst); err != nil {
		t.Errorf("ignored dev config not materialized into worktree: %v", err)
	} else if string(b) != `{"ConnectionStrings":{"Db":"secret"}}` {
		t.Errorf("materialized content = %q, want the base bytes", b)
	}

	if _, err := os.Stat(filepath.Join(backendWT.Path, "obj", "junk.dll")); !os.IsNotExist(err) {
		t.Errorf("build output obj/junk.dll leaked into worktree")
	}

	if strings.Join(backendWT.Materialized, ",") != "appsettings.Development.json" {
		t.Errorf("Materialized = %v, want [appsettings.Development.json]", backendWT.Materialized)
	}
}

// The core re-key: a created stack must NOT become a top-level workspace, but it
// MUST be visible in the workspace's stack list. This fails if Create still
// registered the stack as a workspace.
func TestCreatedStackIsNotAWorkspace(t *testing.T) {
	base := newBase(t)
	if _, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := workspace.FindByName("navexa--feat"); err == nil {
		t.Fatal("stack registered as a top-level workspace; it must live in the workspace's stacks store instead")
	}
	all, err := workspace.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 || all[0].Name != "navexa" {
		t.Fatalf("registry = %+v, want only the base workspace navexa", all)
	}

	stacks, err := List(base.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stacks) != 1 || stacks[0].Name != "navexa--feat" {
		t.Fatalf("List = %+v, want the feat stack visible in the store", stacks)
	}
}

func TestCreateUnknownService(t *testing.T) {
	base := newBase(t)
	if _, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"nope"}}); err == nil {
		t.Fatal("expected error for unknown service")
	}
}

// The pre-flight probe must agree with Remove: it refuses a dirty worktree and
// --force still gets through. A probe that refuses more than Remove does would
// make a removable stack unremovable.
func TestCheckRemovableMatchesRemovesRefusal(t *testing.T) {
	base := newBase(t)

	orig := daemonReachable
	daemonReachable = func(int) bool { return false }
	defer func() { daemonReachable = orig }()

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := CheckRemovable(base, "feat", false); err != nil {
		t.Fatalf("CheckRemovable() = %v on a clean stack, want nil", err)
	}

	writeFile(t, filepath.Join(res.StackRoot, "backend", "scratch.txt"), "wip\n")

	err = CheckRemovable(base, "feat", false)
	if err == nil {
		t.Fatal("CheckRemovable() = nil for a dirty worktree, want a refusal")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("refusal should name the reason: %v", err)
	}
	if err := CheckRemovable(base, "feat", true); err != nil {
		t.Fatalf("CheckRemovable(force) = %v, want nil", err)
	}
}
