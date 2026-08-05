package mcp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/workspace"
)

const configService = `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
config:
  sources:
    - appsettings.json
`

// Two shapes of secret: one the key name gives away, and one hidden inside a
// connection string whose key name is innocent.
const configAppsettings = `{
  "ApiKey": "sk-live-PLAINTEXT-BASE",
  "ConnectionStrings": { "Main": "Server=localhost;Database=app;Password=hunter2-BASE" }
}`

const stackAppsettings = `{
  "ApiKey": "sk-live-PLAINTEXT-STACK",
  "ConnectionStrings": { "Main": "Server=localhost;Database=app;Password=hunter2-STACK" }
}`

// seedConfigWorkspace lays down base as a git repo whose replica can be built,
// plus one feature stack whose worktree holds its own appsettings, and registers
// the workspace so stack.ResolveWorktree can find it.
func seedConfigWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	repo := filepath.Join(root, "api")
	writeFile(t, filepath.Join(repo, config.ServiceManifestFileName), configService)
	writeFile(t, filepath.Join(repo, "appsettings.json"), configAppsettings)
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
	writeFile(t, filepath.Join(worktree, config.ServiceManifestFileName), configService)
	writeFile(t, filepath.Join(worktree, "appsettings.json"), stackAppsettings)

	data, err := json.Marshal([]stack.Record{{
		Name:      "feat",
		Base:      "navexa",
		Root:      stackRoot,
		Worktrees: map[string]string{"api": worktree},
	}})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	storeDir := workspace.DataDir("navexa")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "stacks.json"), data, 0644); err != nil {
		t.Fatalf("write store: %v", err)
	}

	ws := &workspace.Workspace{Name: "navexa", Path: root}
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return ws
}

func stackConfigServer(t *testing.T, ws *workspace.Workspace) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0")
	registerStackConfigTool(s, ws)
	return s
}

// A tool that hands a credential to an agent puts it in a transcript, a log and
// a context window. Both copies are read through the same masking, so neither
// the key-named secret nor the one buried in a connection string comes back.
func TestStackConfigMasksSecrets(t *testing.T) {
	ws := seedConfigWorkspace(t)
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("replica.Ensure: %v", err)
	}
	s := stackConfigServer(t, ws)

	for name, args := range map[string]map[string]any{
		"base":  {"service": "api", "stack": "base"},
		"stack": {"service": "api", "stack": "feat"},
	} {
		out := safetyCallTool(t, s, "stack_config", args)
		for _, leaked := range []string{"sk-live-PLAINTEXT", "hunter2"} {
			if strings.Contains(out, leaked) {
				t.Errorf("%s: stack_config leaked %q into the result:\n%s", name, leaked, out)
			}
		}
		if !strings.Contains(out, "ApiKey") || !strings.Contains(out, "ConnectionStrings__Main") {
			t.Errorf("%s: stack_config must still report the keys:\n%s", name, out)
		}
		if !strings.Contains(out, svcconfig.MaskedValue) {
			t.Errorf("%s: stack_config must mask the secret with %q:\n%s", name, svcconfig.MaskedValue, out)
		}
	}
}

// The read-only tools take base when the stack parameter is absent, and they
// never read the working directory. stack_config follows that convention: an
// absent stack must report exactly what stack="base" reports.
func TestStackConfigAbsentStackIsBase(t *testing.T) {
	ws := seedConfigWorkspace(t)
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("replica.Ensure: %v", err)
	}
	s := stackConfigServer(t, ws)

	absent := safetyCallTool(t, s, "stack_config", map[string]any{"service": "api"})
	named := safetyCallTool(t, s, "stack_config", map[string]any{"service": "api", "stack": "base"})
	if absent != named {
		t.Errorf("an absent stack must read base.\nabsent:\n%s\nstack=base:\n%s", absent, named)
	}
	if !strings.Contains(absent, "in base") || !strings.Contains(absent, replica.Root(ws)) {
		t.Errorf("base's report must name base and the replica it was read from:\n%s", absent)
	}
	if !strings.Contains(safetyCallTool(t, s, "stack_config", map[string]any{"service": "api", "stack": "feat"}), "in stack navexa--feat") {
		t.Error("a named stack must be reported as that stack")
	}
}

// The stack copy is read from the stack's worktree, so its values are the
// worktree's and not base's.
func TestStackConfigReadsTheStackWorktree(t *testing.T) {
	ws := seedConfigWorkspace(t)
	s := stackConfigServer(t, ws)

	out := safetyCallTool(t, s, "stack_config", map[string]any{"service": "api", "stack": "feat"})
	if !strings.Contains(out, "is down") || !strings.Contains(out, "would run with") {
		t.Errorf("a stack that is down must be reported as down:\n%s", out)
	}
	if !strings.Contains(out, "serve_env ladder") {
		t.Errorf("the report must include the environment the process receives:\n%s", out)
	}

	unknown := safetyCallTool(t, s, "stack_config", map[string]any{"service": "nope", "stack": "feat"})
	if !strings.Contains(unknown, "not in stack") || !strings.Contains(unknown, "api") {
		t.Errorf("an unknown service must be refused and the stack's services listed:\n%s", unknown)
	}
}

// stack_config reads files and starts nothing, so it carries the annotations of
// the other read-only tools.
func TestStackConfigIsAnnotatedReadOnly(t *testing.T) {
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	s := stackConfigServer(t, ws)

	tool, ok := listTools(t, s)["stack_config"]
	if !ok {
		t.Fatal("stack_config is not registered")
	}
	if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
		t.Error("stack_config must be annotated read-only")
	}
	if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
		t.Error("stack_config destroys nothing")
	}
	if _, ok := tool.InputSchema.Properties["stack"]; !ok {
		t.Error("stack_config must take a stack parameter")
	}
}
