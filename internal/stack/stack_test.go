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

	stacks, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stacks) != 1 || stacks[0].Name != "navexa--feat" {
		t.Fatalf("List = %+v, want one stack navexa--feat", stacks)
	}
	if stacks[0].BaseName != "navexa" {
		t.Errorf("stack base = %q, want navexa", stacks[0].BaseName)
	}

	rmRes, err := Remove("feat", false)
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

	stacks, err = List()
	if err != nil {
		t.Fatalf("List after rm: %v", err)
	}
	if len(stacks) != 0 {
		t.Errorf("List after rm = %+v, want empty", stacks)
	}
}

func TestCreateRejectsStackAsBase(t *testing.T) {
	base := newBase(t)
	if _, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	stackWS, err := workspace.FindByName("navexa--feat")
	if err != nil {
		t.Fatalf("find stack: %v", err)
	}
	if _, err := Create(CreateInput{Base: stackWS, Name: "nested", Repos: []string{"backend"}}); err == nil {
		t.Fatal("expected Create to reject a stack as base, got nil error")
	}
}

func TestCreateUnknownService(t *testing.T) {
	base := newBase(t)
	if _, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"nope"}}); err == nil {
		t.Fatal("expected error for unknown service")
	}
}
