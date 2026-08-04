package mcp

import (
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

// baseWorkspace lays down a one-service workspace whose service repo is a real
// git checkout, under a HOME of its own. The replica is not built yet.
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
`)
	return &workspace.Workspace{Name: "navexa", Path: root}, repo
}

func baseToolServer(t *testing.T, ws *workspace.Workspace) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0")
	registerBaseTool(s, ws)
	return s
}

// path is the tool an agent reaches for to answer "where does base actually run
// from", so it has to name the replica and not the checkout it was built from.
func TestBaseToolPathReturnsTheReplicaNotTheCheckout(t *testing.T) {
	ws, repo := baseWorkspace(t)
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	s := baseToolServer(t, ws)

	out := callTool(t, s, "base", map[string]string{"action": "path"})
	if !strings.Contains(out, replica.Root(ws)) {
		t.Errorf("action=path must print the replica root %q; got %s", replica.Root(ws), out)
	}

	svc := callTool(t, s, "base", map[string]string{"action": "path", "service": "api"})
	if !strings.Contains(svc, filepath.Join(replica.Root(ws), "api")) {
		t.Errorf("action=path service=api must print that service's worktree; got %s", svc)
	}
	if strings.Contains(svc, repo+`"`) {
		t.Errorf("action=path must not print the checkout %q; got %s", repo, svc)
	}

	unknown := callTool(t, s, "base", map[string]string{"action": "path", "service": "nope"})
	if !strings.Contains(unknown, "not in workspace") || !strings.Contains(unknown, "api") {
		t.Errorf("an unknown service must be refused and the real ones listed; got %s", unknown)
	}
}

// Before 'devstack workspace up' there is no replica at all, and an agent told
// only "failed" would go looking for the wrong thing.
func TestBaseToolSaysWhenNoReplicaIsBuilt(t *testing.T) {
	ws, _ := baseWorkspace(t)
	s := baseToolServer(t, ws)

	out := callTool(t, s, "base", map[string]string{"action": "path"})
	if !strings.Contains(out, "has not built the replica") || !strings.Contains(out, "devstack workspace up") {
		t.Errorf("action=path must say no replica is built and how to build one; got %s", out)
	}
}

// sync is the step that carries a merged commit into what base runs, and the
// before/after SHAs are how an agent knows it moved. Nothing is restarted, so
// the result has to say the running copy is still on the old code.
func TestBaseToolSyncMovesTheReplicaAndSaysNothingRestarted(t *testing.T) {
	ws, repo := baseWorkspace(t)
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	s := baseToolServer(t, ws)

	before := baseGit(t, filepath.Join(replica.Root(ws), "api"), "rev-parse", "--short", "HEAD")
	if out := callTool(t, s, "base", map[string]string{"action": "sync"}); !strings.Contains(out, "Nothing moved") {
		t.Errorf("a replica already at the tip must report that nothing moved; got %s", out)
	}

	writeFile(t, filepath.Join(repo, "next.txt"), "second commit\n")
	baseGit(t, repo, "add", "-f", ".")
	baseGit(t, repo, "commit", "-q", "-m", "second")
	after := baseGit(t, repo, "rev-parse", "--short", "HEAD")

	out := callTool(t, s, "base", map[string]string{"action": "sync"})
	if !strings.Contains(out, before) || !strings.Contains(out, after) {
		t.Errorf("sync must report the move %s → %s; got %s", before, after, out)
	}
	if !strings.Contains(out, "does not restart") && !strings.Contains(out, "Nothing was restarted") {
		t.Errorf("sync must say the running copy still serves the old code; got %s", out)
	}
	if got := baseGit(t, filepath.Join(replica.Root(ws), "api"), "rev-parse", "--short", "HEAD"); got != after {
		t.Errorf("the replica worktree is at %s, want the default branch tip %s", got, after)
	}
}

func TestBaseToolRejectsAnUnknownAction(t *testing.T) {
	ws, _ := baseWorkspace(t)
	out := callTool(t, baseToolServer(t, ws), "base", map[string]string{"action": "move"})
	for _, want := range []string{"unknown action", "path", "sync"} {
		if !strings.Contains(out, want) {
			t.Errorf("an unknown action must be refused and the real ones named (%q); got %s", want, out)
		}
	}
	if strings.Contains(out, "\"build\"") {
		t.Errorf("build is not an action any more, and the tool still offers it; got %s", out)
	}
}

// sync is the one action that writes, so it has to answer the state where there
// is no replica at all. An agent that found none had to leave the tool and go to
// a terminal.
func TestBaseToolSyncBuildsTheReplicaThatIsNotThere(t *testing.T) {
	ws, _ := baseWorkspace(t)
	s := baseToolServer(t, ws)

	out := callTool(t, s, "base", map[string]string{"action": "sync"})
	if !strings.Contains(out, replica.Root(ws)) || !strings.Contains(out, "api") {
		t.Errorf("sync must report the replica root and each worktree it cut; got %s", out)
	}
	if !strings.Contains(out, "no replica yet") {
		t.Errorf("sync must say that it built the replica first; got %s", out)
	}
	if _, err := os.Stat(filepath.Join(replica.Root(ws), "api", ".git")); err != nil {
		t.Fatalf("sync must leave a real worktree at %s: %v", filepath.Join(replica.Root(ws), "api"), err)
	}
	if path := callTool(t, s, "base", map[string]string{"action": "path"}); strings.Contains(path, "has not built the replica") {
		t.Errorf("after sync, action=path must resolve; got %s", path)
	}

	again := callTool(t, s, "base", map[string]string{"action": "sync"})
	if strings.Contains(again, "no replica yet") {
		t.Errorf("a second sync must not build again; got %s", again)
	}
	if !strings.Contains(again, "Nothing moved") {
		t.Errorf("a second sync must report that nothing moved; got %s", again)
	}
}

// sync writes, so the tool cannot claim to be read-only; the description is
// where the split between the two actions is stated.
func TestBaseToolIsAnnotatedForTheActionThatWrites(t *testing.T) {
	ws, _ := baseWorkspace(t)
	tool := listTools(t, baseToolServer(t, ws))["base"]

	if tool.Annotations.ReadOnlyHint == nil || *tool.Annotations.ReadOnlyHint {
		t.Error("base must not be annotated read-only: sync moves the code base runs")
	}
	for _, want := range []string{"action=\"path\" reads only", "action=\"sync\" builds the replica and moves what base runs", "devstack base sync", "it restarts nothing"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("base's description must state %q: %s", want, tool.Description)
		}
	}
}
