package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

// buildStackScenario wires a base workspace (observability on, backend pinned to
// 8080, frontend calling backend) plus a sibling feature stack whose worktrees
// carry the same service manifests, then allocates the stack's backend port.
// Returns the stack workspace and the allocated backend/http port.
func buildStackScenario(t *testing.T) (*workspace.Workspace, int) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot := filepath.Join(tmpHome, "dev", "navexa")
	baseBackend := filepath.Join(baseRoot, "backend")
	baseFrontend := filepath.Join(baseRoot, "frontend")

	stackRoot := filepath.Join(tmpHome, "dev", ".devstack-stacks", "feat")
	stackBackend := filepath.Join(stackRoot, "backend")
	stackFrontend := filepath.Join(stackRoot, "frontend")

	for _, d := range []string{baseBackend, baseFrontend, stackBackend, stackFrontend} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

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
	writeFile(t, filepath.Join(baseBackend, "devstack.service.yaml"), backendSvc)
	writeFile(t, filepath.Join(baseFrontend, "devstack.service.yaml"), frontendSvc)
	writeFile(t, filepath.Join(stackBackend, "devstack.service.yaml"), backendSvc)
	writeFile(t, filepath.Join(stackFrontend, "devstack.service.yaml"), frontendSvc)

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

	writeFile(t, filepath.Join(stackRoot, "devstack.workspace.yaml"), fmt.Sprintf(`version: 1
workspace:
  name: navexa--feat
  repoDiscovery:
    mode: explicit
    repos:
      - %s
      - %s
calls:
  frontend:
    - backend
`, stackBackend, stackFrontend))

	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: baseRoot, TiltPort: 10350}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	if err := workspace.Register(workspace.Workspace{Name: "navexa--feat", Path: stackRoot, TiltPort: 10360, BaseName: "navexa"}); err != nil {
		t.Fatalf("register stack: %v", err)
	}

	allocated, err := workspace.AllocatePorts("navexa--feat", []string{stack.QualifyPortKey("backend", "http")})
	if err != nil {
		t.Fatalf("allocate ports: %v", err)
	}
	port := allocated[stack.QualifyPortKey("backend", "http")]
	if port == 0 {
		t.Fatalf("no port allocated for backend/http")
	}

	ws, err := workspace.FindByName("navexa--feat")
	if err != nil {
		t.Fatalf("find stack: %v", err)
	}
	return ws, port
}

func TestRegenerateTiltfileStackUsesAllocatedPortsAndBaseOtel(t *testing.T) {
	ws, port := buildStackScenario(t)

	path, err := regenerateTiltfile(ws)
	if err != nil {
		t.Fatalf("regenerateTiltfile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Tiltfile: %v", err)
	}
	out := string(data)

	if !strings.Contains(out, fmt.Sprintf("http://localhost:%d", port)) {
		t.Fatalf("Tiltfile does not reference the allocated backend port %d:\n%s", port, out)
	}
	if strings.Contains(out, "8080") {
		t.Fatalf("Tiltfile still references base's pinned port 8080 — overlay allocation did not win:\n%s", out)
	}
	if !strings.Contains(out, `"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317"`) {
		t.Fatalf("Tiltfile does not point OTEL at base's collector (http://localhost:4317):\n%s", out)
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
