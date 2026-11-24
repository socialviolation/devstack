package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

const taskBaseService = `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
ports:
  http: 5001
tasks:
  where:
    run: echo base
`

const taskStackService = `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
ports:
  http: 5001
tasks:
  where:
    run: pwd
  fail:
    run: echo broken; exit 3
  port:
    run: echo "$TARGET"
    env:
      TARGET: ${self.url}
`

func seedTaskWorkspace(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	writeFile(t, filepath.Join(root, "api", config.ServiceManifestFileName), taskBaseService)
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
`)

	stackRoot := filepath.Join(home, "stacks", "feat")
	worktree := filepath.Join(home, "wt", "api")
	writeFile(t, filepath.Join(stackRoot, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa--feat
  repoDiscovery:
    mode: explicit
    repos:
      - `+worktree+`
`)
	writeFile(t, filepath.Join(worktree, config.ServiceManifestFileName), taskStackService)

	data, err := json.Marshal([]stack.Record{{
		Name:      "feat",
		Base:      "navexa",
		Root:      stackRoot,
		Overlay:   []string{"api"},
		Worktrees: map[string]string{"api": worktree},
	}})
	if err != nil {
		t.Fatal(err)
	}
	storeDir := workspace.DataDir("navexa")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "stacks.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	ws := &workspace.Workspace{Name: "navexa", Path: root}
	if err := workspace.Register(*ws); err != nil {
		t.Fatal(err)
	}
	return ws, worktree
}

func taskServer(t *testing.T, ws *workspace.Workspace) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0")
	registerTaskRunTool(s, ws)
	return s
}

func TestTaskRunRunsInTheStackWorktree(t *testing.T) {
	ws, worktree := seedTaskWorkspace(t)
	out := safetyCallTool(t, taskServer(t, ws), "task_run", map[string]any{"stack": "feat", "service": "api", "task": "where"})
	if !strings.Contains(out, "exited 0") || !strings.Contains(out, worktree) {
		t.Errorf("want the task to run in %s:\n%s", worktree, out)
	}
	if strings.Contains(out, `\nbase\n`) {
		t.Errorf("the task ran from base's manifest:\n%s", out)
	}
}

func TestTaskRunPassesTheExitCodeThrough(t *testing.T) {
	ws, _ := seedTaskWorkspace(t)
	out := safetyCallTool(t, taskServer(t, ws), "task_run", map[string]any{"stack": "feat", "service": "api", "task": "fail"})
	if !strings.Contains(out, "exited 3") || !strings.Contains(out, "broken") || !strings.Contains(out, `"isError":true`) {
		t.Errorf("want exit 3 reported as a tool error:\n%s", out)
	}
}

func TestTaskRunResolvesTheStackPorts(t *testing.T) {
	ws, _ := seedTaskWorkspace(t)
	ports, err := workspace.AllocatePorts("navexa--feat", []string{"api/http"})
	if err != nil {
		t.Fatal(err)
	}
	out := safetyCallTool(t, taskServer(t, ws), "task_run", map[string]any{"stack": "feat", "service": "api", "task": "port"})
	want := "http://localhost:" + strconv.Itoa(ports["api/http"])
	if !strings.Contains(out, want) {
		t.Errorf("want ${self.url} to resolve to %s:\n%s", want, out)
	}
}

func TestTaskRunListsTasksWhenTaskIsEmpty(t *testing.T) {
	ws, _ := seedTaskWorkspace(t)
	out := safetyCallTool(t, taskServer(t, ws), "task_run", map[string]any{"stack": "feat", "service": "api"})
	for _, name := range []string{"fail", "port", "where", "run: pwd"} {
		if !strings.Contains(out, name) {
			t.Errorf("list omits %q:\n%s", name, out)
		}
	}
}

func TestTaskRunRefusesAnUndeclaredTask(t *testing.T) {
	ws, _ := seedTaskWorkspace(t)
	out := safetyCallTool(t, taskServer(t, ws), "task_run", map[string]any{"stack": "feat", "service": "api", "task": "rm -rf /"})
	if !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "Declared tasks: fail, port, where") {
		t.Errorf("want a refusal that lists the declared tasks:\n%s", out)
	}
}

func TestTaskRunRefusesBase(t *testing.T) {
	ws, _ := seedTaskWorkspace(t)
	for _, name := range []string{"", "base"} {
		out := safetyCallTool(t, taskServer(t, ws), "task_run", map[string]any{"stack": name, "service": "api", "task": "where"})
		if !strings.Contains(out, "never in base") {
			t.Errorf("stack %q: want a refusal:\n%s", name, out)
		}
	}
}
