package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func TestWriteAgentsMDIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := writeAgentsMD("api", dir, "/home/dev/navexa", ""); err != nil {
		t.Fatalf("writeAgentsMD first: %v", err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err := writeAgentsMD("api", dir, "/home/dev/navexa", ""); err != nil {
		t.Fatalf("writeAgentsMD second: %v", err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(first) != string(second) {
		t.Fatalf("not byte-identical on second run:\n--- first ---\n%q\n--- second ---\n%q", first, second)
	}
	if n := strings.Count(string(second), agentsSentinelBegin); n != 1 {
		t.Fatalf("expected exactly one begin sentinel, got %d", n)
	}
	if !strings.HasSuffix(string(second), "\n") || strings.HasSuffix(string(second), "\n\n") {
		t.Fatalf("expected exactly one trailing newline, got %q", string(second)[len(second)-3:])
	}
}

func TestWriteAgentsMDPreservesBeadsAndMigratesLegacy(t *testing.T) {
	dir := t.TempDir()
	agentsFile := filepath.Join(dir, "AGENTS.md")
	seed := "# api\n\nSome preamble.\n\n" +
		"## Dev Stack (devstack MCP)\n\n" +
		"stale legacy content referencing devstack workspace doctor\n\n" +
		"## BEADS\n\n" +
		"- keep me: bead workflow notes\n"
	if err := os.WriteFile(agentsFile, []byte(seed), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := writeAgentsMD("api", dir, "/home/dev/navexa", ""); err != nil {
		t.Fatalf("writeAgentsMD: %v", err)
	}
	got, _ := os.ReadFile(agentsFile)
	s := string(got)

	if !strings.Contains(s, "## BEADS") || !strings.Contains(s, "keep me: bead workflow notes") {
		t.Fatalf("BEADS block was clobbered:\n%s", s)
	}
	if strings.Count(s, agentsSentinelBegin) != 1 || strings.Count(s, agentsSentinelEnd) != 1 {
		t.Fatalf("expected exactly one sentinel pair:\n%s", s)
	}
	if strings.Count(s, "## Dev Stack (devstack MCP)") != 1 {
		t.Fatalf("expected exactly one devstack section (legacy not stripped):\n%s", s)
	}
	if !strings.Contains(s, "Some preamble.") {
		t.Fatalf("preamble was lost:\n%s", s)
	}

	if err := writeAgentsMD("api", dir, "/home/dev/navexa", ""); err != nil {
		t.Fatalf("writeAgentsMD second: %v", err)
	}
	got2, _ := os.ReadFile(agentsFile)
	if string(got) != string(got2) {
		t.Fatalf("migration not idempotent")
	}
}

func TestBuildAgentInstructionsContentSanity(t *testing.T) {
	block := buildAgentInstructions("api", "/home/dev/navexa/api", "/home/dev/navexa", "")
	if !strings.Contains(block, "<workspace>:<service>") {
		t.Fatalf("missing instance-naming guidance:\n%s", block)
	}
	if !strings.Contains(block, "After you edit code") || !strings.Contains(block, "devstack restart") {
		t.Fatalf("missing hot-reload/restart guidance:\n%s", block)
	}
	if strings.Contains(block, "devstack up") {
		t.Fatalf("generated block references the non-existent `devstack up` (use `devstack workspace up`):\n%s", block)
	}
	for _, want := range []string{"devstack workspace doctor", "devstack otel status", "--stacks"} {
		if !strings.Contains(block, want) {
			t.Fatalf("generated block missing expected real command %q:\n%s", want, block)
		}
	}
}

func readMCPEnv(t *testing.T, mcpFile string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("read %s: %v", mcpFile, err)
	}
	var cfg struct {
		McpServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal %s: %v", mcpFile, err)
	}
	return cfg.McpServers["devstack"].Env
}

func TestWriteMCPJsonOmitsMachineEnvAndIsIdentical(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	f1 := filepath.Join(dir1, ".mcp.json")
	f2 := filepath.Join(dir2, ".mcp.json")

	if err := writeMCPJson(f1, "api"); err != nil {
		t.Fatalf("writeMCPJson(f1): %v", err)
	}
	if err := writeMCPJson(f2, "api"); err != nil {
		t.Fatalf("writeMCPJson(f2): %v", err)
	}

	env := readMCPEnv(t, f1)
	if _, ok := env["DEVSTACK_WORKSPACE"]; ok {
		t.Fatalf("env still contains DEVSTACK_WORKSPACE: %#v", env)
	}
	if _, ok := env["DEVSTACK_DAEMON_PORT"]; ok {
		t.Fatalf("env still contains DEVSTACK_DAEMON_PORT: %#v", env)
	}
	if env["DEVSTACK_DEFAULT_SERVICE"] != "api" {
		t.Fatalf("DEVSTACK_DEFAULT_SERVICE = %q, want %q", env["DEVSTACK_DEFAULT_SERVICE"], "api")
	}

	d1, _ := os.ReadFile(f1)
	d2, _ := os.ReadFile(f2)
	if string(d1) != string(d2) {
		t.Fatalf(".mcp.json differs between two service dirs:\n--- f1 ---\n%s\n--- f2 ---\n%s", d1, d2)
	}
}

// setupBaseAndSiblingStack registers a base workspace and a sibling feature
// stack in a temp registry, and returns their roots and the base service dir.
func setupBaseAndSiblingStack(t *testing.T) (baseRoot, stackRoot, baseServiceDir string) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot = filepath.Join(tmpHome, "dev", "navexa")
	stackRoot = filepath.Join(tmpHome, "dev", "navexa--stack")
	baseServiceDir = filepath.Join(baseRoot, "api")
	stackServiceDir := filepath.Join(stackRoot, "api")

	for _, d := range []string{baseServiceDir, stackServiceDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	writeFile(t, filepath.Join(baseRoot, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
`)
	writeFile(t, filepath.Join(baseServiceDir, "devstack.service.yaml"), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)

	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: baseRoot, TiltPort: 10350}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	if err := workspace.Register(workspace.Workspace{Name: "navexa--stack", Path: stackRoot, TiltPort: 10360}); err != nil {
		t.Fatalf("register stack: %v", err)
	}
	return baseRoot, stackRoot, baseServiceDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The core property: the committed .mcp.json is generated in the base checkout,
// but an agent that starts the MCP server from inside a sibling stack worktree
// (with DEVSTACK_WORKSPACE unset) must resolve to the stack's port, not base's.
func TestServeResolvesStackPortFromCwd(t *testing.T) {
	_, stackRoot, baseServiceDir := setupBaseAndSiblingStack(t)

	mcpFile := filepath.Join(baseServiceDir, ".mcp.json")
	if err := writeMCPJson(mcpFile, "api"); err != nil {
		t.Fatalf("writeMCPJson: %v", err)
	}
	env := readMCPEnv(t, mcpFile)

	t.Chdir(filepath.Join(stackRoot, "api"))

	ws := resolveServeWorkspace(env["DEVSTACK_WORKSPACE"])
	if ws.TiltPort != 10360 {
		t.Fatalf("resolved TiltPort = %d, want 10360 (the stack's port, not base's 10350)", ws.TiltPort)
	}
	if ws.Name != "navexa--stack" {
		t.Fatalf("resolved workspace = %q, want navexa--stack", ws.Name)
	}
}

// A deliberately set DEVSTACK_WORKSPACE still overrides cwd detection: the value
// baked into old committed .mcp.json files therefore keeps working.
func TestServeWorkspaceOverrideWins(t *testing.T) {
	baseRoot, stackRoot, _ := setupBaseAndSiblingStack(t)

	t.Chdir(filepath.Join(stackRoot, "api"))

	ws := resolveServeWorkspace(baseRoot)
	if ws.TiltPort != 10350 {
		t.Fatalf("override resolved TiltPort = %d, want 10350 (base)", ws.TiltPort)
	}
	if ws.Name != "navexa" {
		t.Fatalf("override resolved workspace = %q, want navexa", ws.Name)
	}
}
