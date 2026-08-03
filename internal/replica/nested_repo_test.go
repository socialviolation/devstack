package replica

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

// A service directory that is a plain subdirectory of a repository answers yes
// to "is there a repository here", because rev-parse walks up. git can only cut
// a worktree of a whole repository, so accepting it copies the enclosing
// repository under the service's name and every later resolve fails on the
// missing service manifest. Two of the user's real workspaces are shaped this
// way, so this refusal is the difference between a clear error and a replica
// that breaks Tiltfile generation for the whole machine.
func TestEnsureRefusesAServiceThatIsASubdirectoryOfARepo(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	makeRepo(t, filepath.Join(root, "backend"), backendSvc)

	monorepo := filepath.Join(root, "agent_local")
	writeFile(t, filepath.Join(monorepo, "README.md"), "the repo root\n")
	writeFile(t, filepath.Join(monorepo, "agent-rag", "devstack.service.yaml"), strings.Replace(frontendSvc, "frontend", "agent-rag", 1))
	rungit(t, monorepo, "init", "-b", "main", "-q")
	rungit(t, monorepo, "config", "commit.gpgsign", "false")
	rungit(t, monorepo, "add", "-f", ".")
	rungit(t, monorepo, "commit", "-q", "-m", "init")

	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./agent_local/agent-rag
`)
	ws := &workspace.Workspace{Name: "navexa", Path: root}

	_, err := Ensure(ws)
	if err == nil {
		t.Fatal("Ensure accepted a service directory that is a subdirectory of a repository")
	}
	for _, want := range []string{"agent-rag", "own git repository", monorepo} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q, got: %v", want, err)
		}
	}

	if _, statErr := os.Stat(filepath.Join(Root(ws), "agent-rag")); statErr == nil {
		t.Error("Ensure created a worktree for the refused service")
	}
}

func TestEnsureAcceptsARepoRoot(t *testing.T) {
	ws := newTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure refused a workspace whose services are their own repos: %v", err)
	}
}
