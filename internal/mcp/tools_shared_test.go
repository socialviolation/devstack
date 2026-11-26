package mcp

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

func baseGit(t *testing.T, dir string, args ...string) string {
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

func baseWorkspace(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	repo := filepath.Join(root, "api")
	writeFile(t, filepath.Join(repo, config.ServiceManifestFileName), basicService)
	baseGit(t, repo, "init", "-b", "main", "-q")
	baseGit(t, repo, "config", "commit.gpgsign", "false")
	baseGit(t, repo, "add", "-f", ".")
	baseGit(t, repo, "commit", "-q", "-m", "init")

	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
shared: [api]
`)
	return &workspace.Workspace{Name: "navexa", Path: root}, repo
}

func baseToolServer(t *testing.T, ws *workspace.Workspace) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0")
	registerSharedTool(s, ws)
	return s
}

func TestSharedToolPathReturnsTheReplicaNotTheCheckout(t *testing.T) {
	ws, repo := baseWorkspace(t)
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	s := baseToolServer(t, ws)

	out := callTool(t, s, "shared", map[string]string{"action": "path"})
	if !strings.Contains(out, replica.Root(ws)) {
		t.Errorf("action=path must print the replica root %q; got %s", replica.Root(ws), out)
	}

	svc := callTool(t, s, "shared", map[string]string{"action": "path", "service": "api"})
	if !strings.Contains(svc, filepath.Join(replica.Root(ws), "api")) {
		t.Errorf("action=path service=api must print that service's worktree; got %s", svc)
	}
	if strings.Contains(svc, repo+`"`) {
		t.Errorf("action=path must not print the checkout %q; got %s", repo, svc)
	}

	unknown := callTool(t, s, "shared", map[string]string{"action": "path", "service": "nope"})
	if !strings.Contains(unknown, "is not a shared service") || !strings.Contains(unknown, "api") {
		t.Errorf("an unknown service must be refused and the real ones listed; got %s", unknown)
	}
}

func TestSharedToolSaysWhenNoReplicaIsBuilt(t *testing.T) {
	ws, _ := baseWorkspace(t)
	s := baseToolServer(t, ws)

	out := callTool(t, s, "shared", map[string]string{"action": "path"})
	if !strings.Contains(out, "has not built the replica") || !strings.Contains(out, "devstack workspace up") {
		t.Errorf("action=path must say no replica is built and how to build one; got %s", out)
	}
}

func TestSharedToolSyncMovesTheReplicaAndRestartsOnlyWhatMoved(t *testing.T) {
	ws, repo := baseWorkspace(t)
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	var restarted []string
	orig := sharedRestarter
	sharedRestarter = func(io.Writer, string) func(string) (bool, error) {
		return func(svc string) (bool, error) {
			restarted = append(restarted, svc)
			return true, nil
		}
	}
	t.Cleanup(func() { sharedRestarter = orig })
	s := baseToolServer(t, ws)

	before := baseGit(t, filepath.Join(replica.Root(ws), "api"), "rev-parse", "--short", "HEAD")
	if out := callTool(t, s, "shared", map[string]string{"action": "sync"}); !strings.Contains(out, "Nothing moved") {
		t.Errorf("a replica already at the tip must report that nothing moved; got %s", out)
	}
	if len(restarted) != 0 {
		t.Errorf("a sync that moved nothing restarted %v", restarted)
	}

	writeFile(t, filepath.Join(repo, "next.txt"), "second commit\n")
	baseGit(t, repo, "add", "-f", ".")
	baseGit(t, repo, "commit", "-q", "-m", "second")
	after := baseGit(t, repo, "rev-parse", "--short", "HEAD")

	out := callTool(t, s, "shared", map[string]string{"action": "sync"})
	if !strings.Contains(out, before) || !strings.Contains(out, after) {
		t.Errorf("sync must report the move %s → %s; got %s", before, after, out)
	}
	if !strings.Contains(out, "Restarted: api") || strings.Join(restarted, ",") != "api" {
		t.Errorf("sync must restart the shared service that moved; restarted %v, got %s", restarted, out)
	}
}

func TestSharedToolRejectsAnUnknownAction(t *testing.T) {
	ws, _ := baseWorkspace(t)
	out := callTool(t, baseToolServer(t, ws), "shared", map[string]string{"action": "move"})
	for _, want := range []string{"unknown action", "path", "sync"} {
		if !strings.Contains(out, want) {
			t.Errorf("an unknown action must be refused and the real ones named (%q); got %s", want, out)
		}
	}
	if strings.Contains(out, "\"build\"") {
		t.Errorf("build is not an action any more, and the tool still offers it; got %s", out)
	}
}

func TestSharedToolSyncBuildsTheReplicaThatIsNotThere(t *testing.T) {
	ws, _ := baseWorkspace(t)
	s := baseToolServer(t, ws)

	out := callTool(t, s, "shared", map[string]string{"action": "sync"})
	if !strings.Contains(out, replica.Root(ws)) || !strings.Contains(out, "api") {
		t.Errorf("sync must report the replica root and each worktree it cut; got %s", out)
	}
	if !strings.Contains(out, "no replica yet") {
		t.Errorf("sync must say that it built the replica first; got %s", out)
	}
	if _, err := os.Stat(filepath.Join(replica.Root(ws), "api", ".git")); err != nil {
		t.Fatalf("sync must leave a real worktree at %s: %v", filepath.Join(replica.Root(ws), "api"), err)
	}
	if path := callTool(t, s, "shared", map[string]string{"action": "path"}); strings.Contains(path, "has not built the replica") {
		t.Errorf("after sync, action=path must resolve; got %s", path)
	}

	again := callTool(t, s, "shared", map[string]string{"action": "sync"})
	if strings.Contains(again, "no replica yet") {
		t.Errorf("a second sync must not build again; got %s", again)
	}
	if !strings.Contains(again, "Nothing moved") {
		t.Errorf("a second sync must report that nothing moved; got %s", again)
	}
}

func TestSharedToolIsAnnotatedForTheActionThatWrites(t *testing.T) {
	ws, _ := baseWorkspace(t)
	tool := listTools(t, baseToolServer(t, ws))["shared"]

	if tool.Annotations.ReadOnlyHint == nil || *tool.Annotations.ReadOnlyHint {
		t.Error("shared must not be annotated read-only: sync moves the code the shared set runs")
	}
	for _, want := range []string{"action=\"path\" reads only", "action=\"sync\" writes", "devstack shared sync"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("shared's description must state %q: %s", want, tool.Description)
		}
	}
}
