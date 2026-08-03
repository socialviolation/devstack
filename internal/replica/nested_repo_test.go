package replica

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

// makeMultiServiceRepo builds one repository that holds several services in
// subdirectories, which is the shape of two of the user's real workspaces.
func makeMultiServiceRepo(t *testing.T, dir string, services ...string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "README.md"), "the repository root\n")
	for _, svc := range services {
		writeFile(t, filepath.Join(dir, svc, "devstack.service.yaml"),
			"version: 1\nservice:\n  name: "+svc+"\nruntime:\n  run:\n    command: go run .\n")
	}
	rungit(t, dir, "init", "-b", "main", "-q")
	rungit(t, dir, "config", "commit.gpgsign", "false")
	rungit(t, dir, "add", "-f", ".")
	rungit(t, dir, "commit", "-q", "-m", "init")
}

// A repository can hold several services in subdirectories. git cuts a worktree
// of a whole repository, so the replica must cut ONE worktree for that
// repository and point each service at its directory in it. Cutting one for
// each service copied the enclosing repository under the service's name, and
// every later resolve failed on the missing service manifest.
func TestEnsureCutsOneWorktreeForARepositoryWithTwoServices(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	makeRepo(t, filepath.Join(root, "backend"), backendSvc)
	makeMultiServiceRepo(t, filepath.Join(root, "agent_local"), "agent-rag", "agent-psql")

	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./agent_local/agent-rag
      - ./agent_local/agent-psql
`)
	ws := &workspace.Workspace{Name: "navexa", Path: root}

	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure refused a repository that holds two services: %v", err)
	}

	if got := strings.Join(services(res.Created), ","); got != "agent_local,backend" {
		t.Fatalf("Created = %v, want one worktree per repository: agent_local,backend", services(res.Created))
	}
	var monorepo Worktree
	for _, wt := range res.Created {
		if wt.Repo == "agent_local" {
			monorepo = wt
		}
	}
	if strings.Join(monorepo.Services, ",") != "agent-psql,agent-rag" {
		t.Errorf("the agent_local worktree holds %v, want both of its services", monorepo.Services)
	}
	if monorepo.Path != filepath.Join(res.Root, "agent_local") {
		t.Errorf("worktree path = %q, want it named after the repository", monorepo.Path)
	}

	entries, err := os.ReadDir(res.Root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if strings.Join(dirs, ",") != "agent_local,backend" {
		t.Errorf("replica root holds %v, want one directory per repository", dirs)
	}

	rw, err := config.ResolveWorkspace(res.Root)
	if err != nil {
		t.Fatalf("resolve the generated manifest: %v", err)
	}
	want := map[string]string{
		"backend":    filepath.Join(res.Root, "backend"),
		"agent-rag":  filepath.Join(res.Root, "agent_local", "agent-rag"),
		"agent-psql": filepath.Join(res.Root, "agent_local", "agent-psql"),
	}
	if len(rw.Services) != len(want) {
		t.Fatalf("replica services = %v, want all three", rw.Services)
	}
	for name, path := range want {
		svc, ok := rw.Services[name]
		if !ok {
			t.Fatalf("service %q missing from the replica manifest", name)
		}
		if svc.RepoPath != path {
			t.Errorf("%s RepoPath = %q, want %q", name, svc.RepoPath, path)
		}
		if _, err := os.Stat(filepath.Join(path, "devstack.service.yaml")); err != nil {
			t.Errorf("%s has no service manifest at %s: %v", name, path, err)
		}
	}

	// The worktree is detached at the repository's default branch tip, exactly
	// as a single-service repository is.
	if head := rungit(t, monorepo.Path, "rev-parse", "--abbrev-ref", "HEAD"); head != "HEAD" {
		t.Errorf("agent_local worktree HEAD = %q, want detached", head)
	}
	tip := rungit(t, filepath.Join(root, "agent_local"), "rev-parse", "main")
	if got := rungit(t, monorepo.Path, "rev-parse", "HEAD"); got != tip {
		t.Errorf("agent_local worktree at %s, want the default branch tip %s", got, tip)
	}
}

// A second Ensure must recognise the repository's worktree and leave it where
// it is. Prune-stale works in repository terms, so a directory named after a
// repository is never read as a dropped service.
func TestEnsureIsIdempotentForARepositoryWithTwoServices(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	makeMultiServiceRepo(t, filepath.Join(root, "agent_local"), "agent-rag", "agent-psql")
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./agent_local/agent-rag
      - ./agent_local/agent-psql
`)
	ws := &workspace.Workspace{Name: "navexa", Path: root}

	first, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	before := rungit(t, filepath.Join(first.Root, "agent_local"), "rev-parse", "HEAD")

	second, err := Ensure(ws)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if len(second.Created) != 0 {
		t.Errorf("Created = %v on the second Ensure, want none", services(second.Created))
	}
	if len(second.Removed) != 0 || len(second.Warnings) != 0 {
		t.Errorf("second Ensure removed %v and warned %v, want neither", second.Removed, second.Warnings)
	}
	if after := rungit(t, filepath.Join(first.Root, "agent_local"), "rev-parse", "HEAD"); after != before {
		t.Errorf("the worktree moved on a no-op Ensure: %s -> %s", before, after)
	}
}

// Sync moves the repository's one worktree once and reports every service in
// it, so a caller sees a per-service answer for a per-repository operation.
func TestSyncMovesTheRepositoryOnceAndReportsEveryService(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	repo := filepath.Join(root, "agent_local")
	makeMultiServiceRepo(t, repo, "agent-rag", "agent-psql")
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./agent_local/agent-rag
      - ./agent_local/agent-psql
`)
	ws := &workspace.Workspace{Name: "navexa", Path: root}
	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	writeFile(t, filepath.Join(repo, "new.txt"), "moved on\n")
	rungit(t, repo, "add", "-f", "new.txt")
	rungit(t, repo, "commit", "-q", "-m", "advance main")
	tip := rungit(t, repo, "rev-parse", "--short", "main")

	sync, err := Sync(ws)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sync.Services) != 2 {
		t.Fatalf("Sync reported %d services, want one entry for each service", len(sync.Services))
	}
	for _, s := range sync.Services {
		if s.After != tip {
			t.Errorf("%s synced to %s, want the new tip %s", s.Service, s.After, tip)
		}
		if s.Path != filepath.Join(res.Root, "agent_local", s.Service) {
			t.Errorf("%s Path = %q, want its directory in the repository worktree", s.Service, s.Path)
		}
	}
	if _, err := os.Stat(filepath.Join(res.Root, "agent_local", "new.txt")); err != nil {
		t.Errorf("the new commit's file is missing from the worktree: %v", err)
	}
}

// Two repositories with the same base name in one workspace must not collide.
func TestEnsureSeparatesTwoRepositoriesWithTheSameName(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "dev", "navexa")
	makeRepo(t, filepath.Join(root, "one", "shared"), backendSvc)
	makeRepo(t, filepath.Join(root, "two", "shared"), frontendSvc)
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./one/shared
      - ./two/shared
`)
	ws := &workspace.Workspace{Name: "navexa", Path: root}

	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(res.Created) != 2 {
		t.Fatalf("Created = %v, want one worktree for each repository", services(res.Created))
	}
	if res.Created[0].Path == res.Created[1].Path {
		t.Fatalf("both repositories took the path %s", res.Created[0].Path)
	}

	rw, err := config.ResolveWorkspace(res.Root)
	if err != nil {
		t.Fatalf("resolve the generated manifest: %v", err)
	}
	if len(rw.Services) != 2 {
		t.Errorf("replica services = %v, want both", rw.Services)
	}
	if rw.Services["backend"].RepoPath == rw.Services["frontend"].RepoPath {
		t.Errorf("both services resolve to %s", rw.Services["backend"].RepoPath)
	}
}

func TestEnsureAcceptsARepoRoot(t *testing.T) {
	ws := newTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure refused a workspace whose services are their own repos: %v", err)
	}
}
