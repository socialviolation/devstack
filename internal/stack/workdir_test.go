package stack

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

func serveDirOf(t *testing.T, tiltfile, resource string) string {
	t.Helper()
	lines := strings.Split(tiltfile, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "# "+resource {
			continue
		}
		for _, next := range lines[i:] {
			trimmed := strings.TrimSpace(next)
			if strings.HasPrefix(trimmed, "serve_dir=") {
				return strings.Trim(strings.TrimSuffix(strings.TrimPrefix(trimmed, "serve_dir="), ","), `"`)
			}
		}
	}
	t.Fatalf("the Tiltfile has no serve_dir for %s:\n%s", resource, tiltfile)
	return ""
}

func newAbsWorkDirBase(t *testing.T) *workspace.Workspace {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot := filepath.Join(tmpHome, "dev", "navexa")
	outside := filepath.Join(tmpHome, "dev", "rutter")

	makeRepo(t, filepath.Join(baseRoot, "backend"), fmt.Sprintf(`version: 1
service:
  name: backend
runtime:
  workDir: %s
  run:
    command: mise run dev
`, filepath.Join(baseRoot, "backend")))

	makeRepo(t, filepath.Join(baseRoot, "frontend"), fmt.Sprintf(`version: 1
service:
  name: frontend
runtime:
  workDir: %s
  run:
    command: npm run dev
`, outside))
	makeRepo(t, outside, `version: 1
service:
  name: rutter
runtime:
  run:
    command: go run .
`)

	writeFile(t, filepath.Join(baseRoot, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./frontend
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

// The dangerous half. A stack exists to serve the work in its own worktree, and
// an absolute workDir sent it to base's checkout instead, so the stack served
// code that was never in it.
func TestResolveWorktreePointsAnAbsoluteWorkDirAtTheStack(t *testing.T) {
	base := newAbsWorkDirBase(t)
	orig := daemonReachable
	daemonReachable = func(int) bool { return false }
	defer func() { daemonReachable = orig }()

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend", "frontend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	rw, err := ResolveWorktree(rec)
	if err != nil {
		t.Fatalf("ResolveWorktree: %v", err)
	}

	out, _, err := tiltgen.GenerateHost([]tiltgen.WorkspaceGen{{
		Name:   base.Name,
		Base:   rw,
		Stacks: []tiltgen.StackGen{{Workspace: rw, Namespace: rec.Name}},
	}})
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}

	want := filepath.Join(res.StackRoot, "backend")
	if got := serveDirOf(t, out, "navexa:backend:feat"); got != want {
		t.Errorf("serve_dir = %q, want the stack worktree %q", got, want)
	}

	outside := filepath.Join(filepath.Dir(base.Path), "rutter")
	if got := serveDirOf(t, out, "navexa:frontend:feat"); got != outside {
		t.Errorf("serve_dir = %q, want the unowned directory %q unchanged", got, outside)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "frontend") && strings.Contains(w, outside) {
			found = true
		}
	}
	if !found {
		t.Fatalf("Warnings = %v, want one naming frontend and %s", res.Warnings, outside)
	}
}
