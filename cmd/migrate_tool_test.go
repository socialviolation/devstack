package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

func toolGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// migrateToolWorkspace lays down one workspace with real work for both patches:
// a committed AGENTS.md that holds a block an older devstack wrote, a CLAUDE.md
// that holds nothing else, no .mcp.json, and no replica.
func migrateToolWorkspace(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	svcDir := filepath.Join(root, "api")
	if err := os.MkdirAll(svcDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"),
		"version: 1\nworkspace:\n  name: navexa\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./api\n")
	writeFile(t, filepath.Join(svcDir, "devstack.service.yaml"),
		"version: 1\nservice:\n  name: api\nruntime:\n  run:\n    command: go run .\n")
	writeFile(t, filepath.Join(svcDir, agentsFileName),
		"# api\n\nMine, and it stays.\n\n"+block("Old devstack instructions.")+"\n")
	writeFile(t, filepath.Join(svcDir, "CLAUDE.md"), block("A pointer block.")+"\n")

	toolGit(t, svcDir, "init", "-b", "main", "-q")
	toolGit(t, svcDir, "config", "commit.gpgsign", "false")
	toolGit(t, svcDir, "add", "-f", ".")
	toolGit(t, svcDir, "commit", "-q", "-m", "init")

	ws := &workspace.Workspace{Name: "navexa", Path: root, TiltPort: 10350}
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	return ws, svcDir
}

// migrateToolServer registers the real tool set, with the real patches, exactly
// as `devstack serve` does.
func migrateToolServer(t *testing.T, ws *workspace.Workspace) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0")
	nvxmcp.RegisterTools(s, nil, "", nil, ws.Name, ws.Path, ws, patches())
	return s
}

// migrateToolText returns what an agent receives: the text of the result, and
// not the JSON envelope around it.
func migrateToolText(t *testing.T, s *server.MCPServer, action string) string {
	t.Helper()
	return migrateToolCall(t, s, map[string]any{"action": action})
}

// migrateToolCall passes the arguments through as an agent gives them, so a test
// can reach an argument that migrateToolText does not name.
func migrateToolCall(t *testing.T, s *server.MCPServer, args map[string]any) string {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "migrate", "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s.HandleMessage(context.Background(), req))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Result.Content) == 0 {
		t.Fatalf("the migrate tool returned no content: %s", data)
	}
	var sb strings.Builder
	for _, c := range out.Result.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// The migration is the one command devstack expects an agent to run, so the tool
// has to do the whole of it: preview without touching a repository, then apply
// every patch and hand back the NEXT block that says what is left for a human.
func TestMigrateToolPreviewsThenMigratesARealWorkspace(t *testing.T) {
	ws, svcDir := migrateToolWorkspace(t)
	s := migrateToolServer(t, ws)

	list := migrateToolText(t, s, "list")
	t.Logf("action=list\n%s", list)
	for _, want := range []string{
		"version 1 to 2",
		"this workspace is at version 1, and this devstack needs version 2",
		"This command changes nothing.",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("action=list never states %q:\n%s", want, list)
		}
	}
	if got := readString(t, filepath.Join(svcDir, agentsFileName)); !strings.Contains(got, agentsSentinelBegin) {
		t.Error("action=list removed the devstack block")
	}
	if _, err := os.Stat(filepath.Join(svcDir, ".mcp.json")); !os.IsNotExist(err) {
		t.Errorf("action=list wrote .mcp.json (stat err = %v)", err)
	}
	run := migrateToolText(t, s, "run")
	t.Logf("action=run\n%s", run)

	got := readString(t, filepath.Join(svcDir, agentsFileName))
	if strings.Contains(got, agentsSentinelBegin) {
		t.Errorf("the run left the devstack block in AGENTS.md:\n%s", got)
	}
	if !strings.Contains(got, "Mine, and it stays.") {
		t.Errorf("the run took away text a human wrote:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(svcDir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("CLAUDE.md held devstack content only, so the run must delete it (stat err = %v)", err)
	}
	for _, rel := range []string{".mcp.json", claudeSettingsRel} {
		if _, err := os.Stat(filepath.Join(svcDir, rel)); err != nil {
			t.Errorf("the run did not write %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(replica.Root(ws)); !os.IsNotExist(err) {
		t.Errorf("a migration builds no replica, and this run built one (stat err = %v)", err)
	}

	for _, want := range []string{
		"NEXT",
		"NOW COMMIT",
		commitCommand,
		"restart the session",
		"is at version 2 now",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("action=run never states %q:\n%s", want, run)
		}
	}

	after := migrateToolText(t, s, "list")
	t.Logf("action=list, after the run\n%s", after)
	if !strings.Contains(after, "nothing to do: this workspace is at version 2") {
		t.Errorf("the migration is applied, and the list does not say so:\n%s", after)
	}
}
