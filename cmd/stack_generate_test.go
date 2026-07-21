package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tiltgen"
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

func TestRegenerateTiltfileFoldsActiveStack(t *testing.T) {
	rec, port := buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}

	path, err := regenerateTiltfile(base)
	if err != nil {
		t.Fatalf("regenerateTiltfile (inactive): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Tiltfile: %v", err)
	}
	if strings.Contains(string(data), "backend:feat") {
		t.Fatalf("inactive stack leaked into base Tiltfile:\n%s", data)
	}

	if err := stack.SetActive(base.Name, rec.Name, true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	path, err = regenerateTiltfile(base)
	if err != nil {
		t.Fatalf("regenerateTiltfile (active): %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Tiltfile: %v", err)
	}
	out := string(data)

	if !strings.Contains(out, "backend:feat") {
		t.Fatalf("active stack not folded into base Tiltfile as a namespaced resource:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("http://localhost:%d", port)) {
		t.Fatalf("base Tiltfile does not reference the stack's allocated backend port %d:\n%s", port, out)
	}
	if !strings.Contains(out, `"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317"`) {
		t.Fatalf("stack overlay does not point OTEL at base's collector (http://localhost:4317):\n%s", out)
	}
}

// A non-stack workspace must generate byte-identically to the pre-stack path:
// tiltgen.Generate with only the workspace's own ManagedEnv and no Book override.
func TestRegenerateTiltfileNonStackIsIdentical(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	root := filepath.Join(tmpHome, "dev", "solo")
	svcDir := filepath.Join(root, "api")
	if err := os.MkdirAll(svcDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(svcDir, "devstack.service.yaml"), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
ports:
  http: 9000
env:
  values:
    PORT: "${self.port.http}"
`)
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: solo
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
`)
	if err := workspace.Register(workspace.Workspace{Name: "solo", Path: root, TiltPort: 10350}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ws, err := workspace.FindByName("solo")
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	path, err := regenerateTiltfile(ws)
	if err != nil {
		t.Fatalf("regenerateTiltfile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}
	want, err := tiltgen.Generate(rw, tiltgen.Options{ManagedEnv: workspace.ManagedEnv(ws, names)})
	if err != nil {
		t.Fatalf("reference generate: %v", err)
	}

	if string(got) != want {
		t.Fatalf("non-stack Tiltfile diverged from the pre-stack generation path:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
