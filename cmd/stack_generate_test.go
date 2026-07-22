package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func gitCmd(t *testing.T, dir string, args ...string) {
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

func makeStackRepo(t *testing.T, dir, manifest string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	writeFile(t, filepath.Join(dir, "devstack.service.yaml"), manifest)
	gitCmd(t, dir, "init", "-q")
	gitCmd(t, dir, "add", "-f", ".")
	gitCmd(t, dir, "commit", "-q", "-m", "init")
}

// buildStackScenario wires a base workspace (observability on, backend pinned to
// 8080, frontend calling backend), then creates a real feature stack via
// stack.Create (worktrees, generated manifest, allocated ports, stored record).
// Returns the stack record and the allocated backend/http port.
func buildStackScenario(t *testing.T) (*stack.Record, int) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot := filepath.Join(tmpHome, "dev", "navexa")
	backendSvc := `version: 1
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
	frontendSvc := `version: 1
service:
  name: frontend
runtime:
  run:
    command: npm run dev
env:
  values:
    BACKEND_URL: "${backend.url}"
`
	makeStackRepo(t, filepath.Join(baseRoot, "backend"), backendSvc)
	makeStackRepo(t, filepath.Join(baseRoot, "frontend"), frontendSvc)

	writeFile(t, filepath.Join(baseRoot, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./frontend
observability:
  enabled: true
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

	if _, err := stack.Create(stack.CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}}); err != nil {
		t.Fatalf("stack.Create: %v", err)
	}
	rec, err := stack.Resolve("navexa", "feat")
	if err != nil {
		t.Fatalf("resolve stack: %v", err)
	}
	port := rec.Ports[stack.QualifyPortKey("backend", "http")]
	if port == 0 {
		t.Fatalf("no port allocated for backend/http: %v", rec.Ports)
	}
	return rec, port
}
