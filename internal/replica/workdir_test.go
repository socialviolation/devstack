package replica

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

// serveDirOf reads back the directory the generator makes the service run in.
// That directory is what executes, so it is the only proof the rewrite reaches
// the process.
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

func absWorkDirTemplate(t *testing.T) *workspace.Workspace {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	outside := filepath.Join(home, "dev", "rutter")

	makeRepo(t, filepath.Join(root, "backend"), fmt.Sprintf(`version: 1
service:
  name: backend
runtime:
  workDir: %s
  run:
    command: mise run dev
`, filepath.Join(root, "backend")))

	makeRepo(t, filepath.Join(root, "frontend"), fmt.Sprintf(`version: 1
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

	writeWorkspaceManifest(t, root, "backend", "frontend")
	return &workspace.Workspace{Name: "navexa", Path: root}
}

// A manifest can carry an absolute workDir. Before the rewrite the replica ran
// that service out of the checkout, so base served code nobody had built the
// replica from.
func TestResolvePointsAnAbsoluteWorkDirAtTheReplica(t *testing.T) {
	ws := absWorkDirTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	rw, err := Resolve(ws)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	out, _, err := tiltgen.GenerateHost([]tiltgen.WorkspaceGen{{Name: ws.Name, Base: rw}})
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}

	want := filepath.Join(Root(ws), "backend")
	if got := serveDirOf(t, out, "navexa:backend"); got != want {
		t.Errorf("serve_dir = %q, want the replica worktree %q", got, want)
	}
}

// The other half: a workDir in a repository the workspace does not hold. The
// replica has no copy of it, so the path stays, and Ensure says so.
func TestEnsureWarnsAboutAWorkDirTheWorkspaceDoesNotOwn(t *testing.T) {
	ws := absWorkDirTemplate(t)
	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	outside := filepath.Join(filepath.Dir(ws.Path), "rutter")
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "frontend") && strings.Contains(w, outside) {
			found = true
		}
	}
	if !found {
		t.Fatalf("Warnings = %v, want one naming frontend and %s", res.Warnings, outside)
	}

	rw, err := Resolve(ws)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	out, _, err := tiltgen.GenerateHost([]tiltgen.WorkspaceGen{{Name: ws.Name, Base: rw}})
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}
	if got := serveDirOf(t, out, "navexa:frontend"); got != outside {
		t.Errorf("serve_dir = %q, want the unowned directory %q unchanged", got, outside)
	}
}
