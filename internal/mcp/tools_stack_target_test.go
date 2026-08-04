package mcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// serveHostDaemon answers the daemon view on a port of its own, and points
// workspace.HostTiltPort at it for the length of the test, so the
// DaemonReachable check of resolveLocalTarget succeeds. The real daemon of the
// machine holds the default port, so a test that bound it would never run here.
func serveHostDaemon(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	previous := workspace.HostTiltPort
	workspace.HostTiltPort = l.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { workspace.HostTiltPort = previous })

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"uiResources":[]}`))
	}))
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
}

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

	path, instance, _, err := serviceEnvTarget(ws, basePath, "")
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

	path, instance, _, err := serviceEnvTarget(ws, basePath, "feat")
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

	stackRoot, _, _, err := serviceEnvTarget(ws, basePath, "feat")
	if err != nil {
		t.Fatalf("serviceEnvTarget: %v", err)
	}

	res, err := handleServiceEnvSet(ws, stackRoot, "", "api", "SQL_CONN", "postgres://stack", "manifest")
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

	path, instance, _, err := serviceEnvTarget(ws, basePath, "")
	if err != nil {
		t.Fatalf("serviceEnvTarget: %v", err)
	}
	if instance != "" {
		t.Fatalf("base target must carry no instance label, got %q", instance)
	}

	res, err := handleServiceEnvSet(ws, path, "", "api", "SQL_CONN", "postgres://base", "manifest")
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

	serveHostDaemon(t)

	if err := stack.SetActive("testws", "feat", true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	baseClient := tilt.NewClient("localhost", workspace.HostTiltPort)
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
	if rn := resourceName(ws.Name, "api", got.namespace); rn != "testws:api:feat" {
		t.Errorf("resourceName = %q, want the host-namespaced resource %q", rn, "testws:api:feat")
	}
}

// resourceName must match tiltgen's hostName scheme exactly: <ws>:<svc> for a base
// service and <ws>:<svc>:<stack> for a stack overlay. Drift here means the
// daemon-facing tools would address resources the host daemon does not have.
func TestResourceNameHostScheme(t *testing.T) {
	if got := resourceName("navexa", "api", ""); got != "navexa:api" {
		t.Errorf("resourceName(navexa, api, \"\") = %q, want %q", got, "navexa:api")
	}
	if got := resourceName("navexa", "api", "perf"); got != "navexa:api:perf" {
		t.Errorf("resourceName(navexa, api, perf) = %q, want %q", got, "navexa:api:perf")
	}
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

func TestStackResourceNamesScopedToWorkspace(t *testing.T) {
	mk := func(name string) tilt.UIResource {
		var r tilt.UIResource
		r.Metadata.Name = name
		return r
	}
	view := &tilt.TiltView{UiResources: []tilt.UIResource{
		mk("navexa:api"), mk("navexa:web"), mk("navexa:api:perf"),
		mk("other:api"), mk("other:api:perf"),
	}}

	base := stackResourceNames(view, "navexa", "")
	if want := []string{"navexa:api", "navexa:web"}; !equalStrings(base, want) {
		t.Errorf("base scope = %v, want %v (must exclude stack overlays and other workspaces)", base, want)
	}

	perf := stackResourceNames(view, "navexa", "perf")
	if want := []string{"navexa:api:perf"}; !equalStrings(perf, want) {
		t.Errorf("stack scope = %v, want %v (must exclude other workspaces' perf)", perf, want)
	}

	if _, _, ok := splitHostResource("other:api", "navexa:"); ok {
		t.Errorf("splitHostResource must reject a resource from another workspace")
	}
	svc, ns, ok := splitHostResource("navexa:api:perf", "navexa:")
	if !ok || svc != "api" || ns != "perf" {
		t.Errorf("splitHostResource(navexa:api:perf) = (%q,%q,%v), want (api,perf,true)", svc, ns, ok)
	}
}

// A tool that starts or stops something takes no implicit base: with no stack
// parameter and a working directory inside neither a stack nor the replica, it
// refuses and names what to pass.
func TestMutatingToolRefusesWithoutATarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	registerStopTool(s, nil, "api", &config.WorkspaceConfig{}, ws)

	out := safetyCallTool(t, s, "stop", map[string]any{"service": "api"})
	if !strings.Contains(out, "--stack base") {
		t.Errorf("stop must refuse without a target and name base, got: %s", out)
	}
	if strings.Contains(out, "Stopped") {
		t.Errorf("stop must not have stopped anything, got: %s", out)
	}
}

// The regression the rule most easily causes: a read-only tool changes nothing,
// so absent still means base and it must answer with no stack parameter. It
// fails here on the dead daemon port, which is the point — it got past target
// resolution.
func TestReadOnlyToolNeedsNoTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	registerStatusTool(s, tilt.NewClient("localhost", 1), map[string]string{}, &config.WorkspaceConfig{}, ws)

	out := safetyCallTool(t, s, "status", map[string]any{})
	if strings.Contains(out, "--stack base") {
		t.Errorf("status must not demand a target, got: %s", out)
	}
	if !strings.Contains(out, "dev daemon is not running") {
		t.Errorf("status should have reached the daemon and reported it down, got: %s", out)
	}
}

// The stack parameter means different things on tools that read and tools that
// act, so the two descriptions have to say so where an agent reads them.
func TestMutatingToolsDocumentThatAbsentIsNotBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	cfg := &config.WorkspaceConfig{}
	registerStatusTool(s, nil, nil, cfg, ws)
	registerStartTool(s, nil, "api", cfg, ws)
	registerStopTool(s, nil, "api", cfg, ws)
	registerRestartTool(s, nil, "api", cfg, ws)
	registerEnvTools(s, ws, ws.Path)

	tools := listTools(t, s)
	for _, name := range []string{"start", "stop", "restart", "env_use"} {
		desc := tools[name].InputSchema.Properties["stack"].Description
		if !strings.Contains(desc, "NO implicit default") {
			t.Errorf("%s's stack parameter must say it has no default: %s", name, desc)
		}
		if !strings.Contains(desc, "replica") {
			t.Errorf("%s's stack parameter must say what base runs from: %s", name, desc)
		}
	}
	if desc := tools["status"].InputSchema.Properties["stack"].Description; !strings.Contains(desc, "Absent (or the literal \"base\")") {
		t.Errorf("a read-only tool must keep its base default: %s", desc)
	}
}

// The PATH and BRANCH columns of status are read to answer "is my work in the
// copy that is running". A stack's rows must therefore come from the stack's own
// worktrees; base's serviceDirs, carried over, made every row report base's
// directory and base's branch — a confident wrong answer.
func TestStackTargetServiceDirsAreTheStacksWorktrees(t *testing.T) {
	ws, basePath, worktree := seedStack(t)

	serveHostDaemon(t)

	if err := stack.SetActive("testws", "feat", true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	baseDirs := map[string]string{"api": filepath.Join(basePath, "repos", "api")}
	got, err := resolveLocalTarget(ws, localTarget{serviceDirs: baseDirs}, "feat")
	if err != nil {
		t.Fatalf("resolveLocalTarget: %v", err)
	}
	if got.serviceDirs["api"] != worktree {
		t.Errorf("stack target's serviceDirs[api] = %q, want the stack worktree %q", got.serviceDirs["api"], worktree)
	}
	if got.serviceDirs["api"] == baseDirs["api"] {
		t.Errorf("a stack row must not report base's directory %q", baseDirs["api"])
	}
}

// Base's rows have the same job, and base does not run from the checkout: it
// runs the replica worktrees, which sit on the default branch tip while the
// checkout can be on any branch, dirty.
func TestBaseServiceDirsResolveToTheReplicaWorktrees(t *testing.T) {
	ws, repo := baseWorkspace(t)
	checkouts := map[string]string{"api": repo}

	if got := replicaServiceDirs(ws, checkouts); got["api"] != repo {
		t.Errorf("with no replica built the checkout is what runs; got %q", got["api"])
	}

	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	want := filepath.Join(replica.Root(ws), "api")
	if got := replicaServiceDirs(ws, checkouts)["api"]; got != want {
		t.Errorf("base serviceDirs[api] = %q, want the replica worktree %q", got, want)
	}
}

// The wiring, not just the resolution: the row status prints for a base service
// names the replica worktree.
func TestStatusRowsShowTheReplicaDirectory(t *testing.T) {
	ws, repo := baseWorkspace(t)
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"uiResources":[{"metadata":{"name":"navexa:api"},"status":{"runtimeStatus":"ok"}}]}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	s := server.NewMCPServer("test", "0.0.0")
	registerStatusTool(s, tilt.NewClient(u.Hostname(), port), map[string]string{"api": repo}, &config.WorkspaceConfig{}, ws)

	out := safetyCallTool(t, s, "status", map[string]any{})
	if want := statusPathCell(filepath.Join(replica.Root(ws), "api")); !strings.Contains(out, want) {
		t.Errorf("status must show the replica worktree %q; got %s", want, out)
	}
	if cell := statusPathCell(repo); strings.Contains(out, cell) {
		t.Errorf("status must not report the checkout %q as what base runs; got %s", cell, out)
	}
}

// statusPathCell is how renderColumns lays one path into the PATH column: paths
// longer than the cap are truncated, so the assertion has to compare what is
// printed rather than the full path.
func statusPathCell(path string) string {
	p := []rune(shortenPath(path))
	if len(p) > maxColWidth {
		return string(append(p[:maxColWidth-1:maxColWidth-1], '…'))
	}
	return string(p)
}

// A group half inside a stack resolves, against the stack's manifest, to that
// half — and says nothing about the rest, which keeps serving from base. The
// count and the names of what was left alone belong in the text the agent reads.
func TestTargetGroupMembersReportsTheShortfallInAStack(t *testing.T) {
	ws, basePath, _ := seedStack(t)
	writeFile(t, filepath.Join(basePath, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
groups:
  core:
    - api
    - frontend
    - orbit
`)

	t.Run("half in the stack", func(t *testing.T) {
		target := localTarget{
			cfg:       &config.WorkspaceConfig{Groups: map[string][]string{"core": {"api"}}},
			namespace: "feat",
		}
		members, note, err := targetGroupMembers(ws, target, "core")
		if err != nil {
			t.Fatalf("targetGroupMembers: %v", err)
		}
		if !equalStrings(members, []string{"api"}) {
			t.Errorf("members = %v, want the stack's half %v", members, []string{"api"})
		}
		// The CLI prints Coverage.Sentence() for the same fact, so the tool has
		// to quote it rather than word the shortfall its own way.
		shared := stack.Coverage{Group: "core", In: []string{"api"}, Missing: []string{"frontend", "orbit"}}.Sentence()
		for _, want := range []string{shared, `stack="base"`, "does not touch them"} {
			if !strings.Contains(note, want) {
				t.Errorf("the shortfall note must state %q; got %q", want, note)
			}
		}
	})

	t.Run("none of it in the stack", func(t *testing.T) {
		target := localTarget{
			cfg:       &config.WorkspaceConfig{Groups: map[string][]string{}},
			namespace: "feat",
		}
		_, _, err := targetGroupMembers(ws, target, "core")
		if err == nil {
			t.Fatal("a group with no member in the stack must be refused")
		}
		if strings.Contains(err.Error(), "not found") {
			t.Errorf("a group that exists on base must not read as a typo: %v", err)
		}
		for _, want := range []string{"runs entirely on base", "api, frontend, orbit", `stack="base"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal must state %q; got %v", want, err)
			}
		}
	})

	t.Run("a name that is nowhere", func(t *testing.T) {
		target := localTarget{
			cfg:       &config.WorkspaceConfig{Groups: map[string][]string{"core": {"api"}}},
			namespace: "feat",
		}
		_, _, err := targetGroupMembers(ws, target, "nope")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("an unknown group must still be reported as unknown, got %v", err)
		}
	})
}

// Base is not a stack, so nothing is ever missing from it: the same call there
// keeps today's members and adds no note.
func TestTargetGroupMembersOnBaseIsUnchanged(t *testing.T) {
	ws, _, _ := seedStack(t)
	target := localTarget{cfg: &config.WorkspaceConfig{Groups: map[string][]string{"core": {"api", "frontend"}}}}

	members, note, err := targetGroupMembers(ws, target, "core")
	if err != nil {
		t.Fatalf("targetGroupMembers: %v", err)
	}
	if !equalStrings(members, []string{"api", "frontend"}) || note != "" {
		t.Errorf("base target = (%v, %q), want every member and no note", members, note)
	}
	if _, _, err := targetGroupMembers(ws, target, "nope"); err == nil || !strings.Contains(err.Error(), "available groups: core") {
		t.Errorf("an unknown group on base must list the groups, got %v", err)
	}
}

// The group note has to reach the agent through the tool, not just the helper.
func TestStopReturnsTheGroupShortfallToTheAgent(t *testing.T) {
	ws, basePath, worktree := seedStack(t)
	writeFile(t, filepath.Join(basePath, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
groups:
  core:
    - api
    - frontend
`)
	rec, err := stack.FindStack("testws", "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	writeFile(t, filepath.Join(rec.Root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws--feat
  repoDiscovery:
    mode: explicit
    repos:
      - `+worktree+`
groups:
  core:
    - api
`)
	serveHostDaemon(t)
	if err := stack.SetActive("testws", "feat", true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	s := server.NewMCPServer("test", "0.0.0")
	registerStopTool(s, tilt.NewClient("localhost", workspace.HostTiltPort), "", &config.WorkspaceConfig{}, ws)

	out := safetyCallTool(t, s, "stop", map[string]any{"group": "core", "stack": "feat"})
	shared := stack.Coverage{Group: "core", In: []string{"api"}, Missing: []string{"frontend"}}.Sentence()
	if !strings.Contains(out, shared) {
		t.Errorf("stop must return the group's shortfall in its text, worded as the CLI words it (%q); got %s", shared, out)
	}
}

func listTools(t *testing.T, s *server.MCPServer) map[string]listedTool {
	t.Helper()
	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result struct {
			Tools []listedTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal tools/list: %v (%s)", err, data)
	}
	out := map[string]listedTool{}
	for _, tool := range envelope.Result.Tools {
		out[tool.Name] = tool
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
