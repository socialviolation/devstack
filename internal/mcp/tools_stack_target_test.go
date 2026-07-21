package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

const targetService = `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`

// seedStack lays down a base workspace, a worktree of "api", and a synthesised
// stack root whose generated manifest points at that worktree, then records the
// stack (inactive) in the base's store under a temp HOME. It returns the base
// workspace, its path, and the worktree path.
func seedStack(t *testing.T) (*workspace.Workspace, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := t.TempDir()
	basePath := filepath.Join(root, "base")
	stackRoot := filepath.Join(root, "stackroot")
	worktree := filepath.Join(root, "wt", "api")

	writeFile(t, filepath.Join(basePath, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
`)
	writeFile(t, filepath.Join(basePath, "repos", "api", config.ServiceManifestFileName), targetService)

	writeFile(t, filepath.Join(stackRoot, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws--feat
  repoDiscovery:
    mode: explicit
    repos:
      - `+worktree+`
`)
	writeFile(t, filepath.Join(worktree, config.ServiceManifestFileName), targetService)

	rec := stack.Record{
		Name:      "feat",
		Base:      "testws",
		Root:      stackRoot,
		Worktrees: map[string]string{"api": worktree},
	}
	data, err := json.Marshal([]stack.Record{rec})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	storeDir := workspace.DataDir("testws")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "stacks.json"), data, 0644); err != nil {
		t.Fatalf("write store: %v", err)
	}

	ws := &workspace.Workspace{Name: "testws", Path: basePath}
	return ws, basePath, worktree
}

// Absent stack param resolves to the base workspace, byte-for-byte today's path.
func TestServiceEnvTargetBaseWhenNoStack(t *testing.T) {
	ws, basePath, _ := seedStack(t)

	path, instance, err := serviceEnvTarget(ws, basePath, "")
	if err != nil {
		t.Fatalf("serviceEnvTarget: %v", err)
	}
	if path != basePath {
		t.Errorf("path = %q, want base %q", path, basePath)
	}
	if instance != "" {
		t.Errorf("instance = %q, want empty for base", instance)
	}
}

// A named stack resolves to the stack's synthesised root (worktree-backed).
func TestServiceEnvTargetResolvesStackRoot(t *testing.T) {
	ws, basePath, _ := seedStack(t)

	rec, err := stack.FindStack("testws", "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}

	path, instance, err := serviceEnvTarget(ws, basePath, "feat")
	if err != nil {
		t.Fatalf("serviceEnvTarget: %v", err)
	}
	if path != rec.Root {
		t.Errorf("path = %q, want stack root %q", path, rec.Root)
	}
	if !strings.Contains(instance, "feat") {
		t.Errorf("instance = %q, want it to name the stack", instance)
	}
}

// An unknown stack errors, naming the available stacks so the agent can retry.
func TestResolveStackRecordUnknownNamesAvailable(t *testing.T) {
	ws, _, _ := seedStack(t)

	_, err := resolveStackRecord(ws, "nope")
	if err == nil {
		t.Fatal("expected an error for an unknown stack")
	}
	if !strings.Contains(err.Error(), "feat") {
		t.Errorf("error should list the available stack 'feat', got: %v", err)
	}
}

// The crux mutation: service_env set targeting a stack writes the STACK's
// worktree manifest, and never base's. Dropping the resolution (targeting base)
// would edit the wrong repo — proven by the base-target arm.
func TestServiceEnvSetStackWritesWorktreeNotBase(t *testing.T) {
	ws, basePath, worktree := seedStack(t)

	stackRoot, _, err := serviceEnvTarget(ws, basePath, "feat")
	if err != nil {
		t.Fatalf("serviceEnvTarget: %v", err)
	}

	res, err := handleServiceEnvSet(ws, stackRoot, "api", "SQL_CONN", "postgres://stack", "manifest")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.IsError {
		t.Fatalf("set failed: %s", resultText(t, res))
	}

	wt, err := config.LoadServiceManifest(worktree)
	if err != nil {
		t.Fatalf("load worktree manifest: %v", err)
	}
	if got := wt.Env.Values["SQL_CONN"]; got != "postgres://stack" {
		t.Errorf("stack worktree manifest SQL_CONN = %q, want the written value", got)
	}

	base, err := config.LoadServiceManifest(filepath.Join(basePath, "repos", "api"))
	if err != nil {
		t.Fatalf("load base manifest: %v", err)
	}
	if _, ok := base.Env.Values["SQL_CONN"]; ok {
		t.Error("set stack=feat leaked the write into the BASE repo manifest")
	}
}

// Same tool, no stack param, still writes base — the non-stack path is untouched.
func TestServiceEnvSetBaseWritesBaseRepo(t *testing.T) {
	ws, basePath, worktree := seedStack(t)

	path, instance, err := serviceEnvTarget(ws, basePath, "")
	if err != nil {
		t.Fatalf("serviceEnvTarget: %v", err)
	}
	if instance != "" {
		t.Fatalf("base target must carry no instance label, got %q", instance)
	}

	res, err := handleServiceEnvSet(ws, path, "api", "SQL_CONN", "postgres://base", "manifest")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.IsError {
		t.Fatalf("set failed: %s", resultText(t, res))
	}

	base, err := config.LoadServiceManifest(filepath.Join(basePath, "repos", "api"))
	if err != nil {
		t.Fatalf("load base manifest: %v", err)
	}
	if got := base.Env.Values["SQL_CONN"]; got != "postgres://base" {
		t.Errorf("base repo manifest SQL_CONN = %q, want the written value", got)
	}

	wt, err := config.LoadServiceManifest(worktree)
	if err != nil {
		t.Fatalf("load worktree manifest: %v", err)
	}
	if _, ok := wt.Env.Values["SQL_CONN"]; ok {
		t.Error("a set with no stack param leaked into the stack worktree manifest")
	}
}

// A daemon-facing tool targeting a stack that is not up (base daemon down, or the
// stack inactive) fails fast with the "not up" guidance — never a hang.
func TestResolveLocalTargetStackNotUp(t *testing.T) {
	ws, _, _ := seedStack(t)

	_, err := resolveLocalTarget(ws, localTarget{}, "feat")
	if err == nil {
		t.Fatal("expected an error when the stack is not up")
	}
	if !strings.Contains(err.Error(), "not up") || !strings.Contains(err.Error(), "devstack stack up feat") {
		t.Errorf("error should give the 'not up' guidance, got: %v", err)
	}
}

// When base's daemon is up and the stack is active, the daemon-facing target
// reuses base's client (one daemon) and carries the stack's namespace — so tools
// operate on <service>:<stack> resources on the base port, never a dead per-stack
// port. Mutating resolveLocalTarget to drop the namespace fails the namespace
// assertion; dialing a per-stack client would break the client-identity assertion.
func TestResolveLocalTargetActiveStackReusesBaseClientAndNamespaces(t *testing.T) {
	ws, _, _ := seedStack(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"uiResources":[]}`))
	}))
	defer srv.Close()
	ws.TiltPort = portFromURL(t, srv.URL)

	if err := stack.SetActive("testws", "feat", true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	baseClient := tilt.NewClient("localhost", ws.TiltPort)
	got, err := resolveLocalTarget(ws, localTarget{client: baseClient}, "feat")
	if err != nil {
		t.Fatalf("resolveLocalTarget: %v", err)
	}
	if got.namespace != "feat" {
		t.Errorf("namespace = %q, want the stack name %q", got.namespace, "feat")
	}
	if got.client != baseClient {
		t.Error("stack target must reuse base's client (one daemon), not dial a per-stack port")
	}
	if !strings.Contains(got.label, "feat") {
		t.Errorf("label = %q, want it to name the stack", got.label)
	}
	if rn := resourceName("api", got.namespace); rn != "api:feat" {
		t.Errorf("resourceName = %q, want the namespaced resource %q", rn, "api:feat")
	}
}

// portFromURL extracts the integer port from an http://host:port URL.
func portFromURL(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port from %q: %v", raw, err)
	}
	return p
}

// Absent stack param returns the base target unchanged (byte-identical path).
func TestResolveLocalTargetBasePassthrough(t *testing.T) {
	ws, _, _ := seedStack(t)

	base := localTarget{label: "", defaultSvc: "api"}
	got, err := resolveLocalTarget(ws, base, "")
	if err != nil {
		t.Fatalf("resolveLocalTarget: %v", err)
	}
	if got.label != "" || got.defaultSvc != "api" {
		t.Errorf("base passthrough altered the target: %+v", got)
	}
}
