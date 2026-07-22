package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newTestWorkspace lays down a workspace with a single "api" service.
// observability toggles whether devstack computes rung 6 (OTEL) for it.
func newTestWorkspace(t *testing.T, observability bool, serviceManifest string) (*workspace.Workspace, string, string) {
	t.Helper()
	root := t.TempDir()
	apiDir := filepath.Join(root, "repos", "api")

	obs := ""
	if observability {
		obs = "observability:\n  enabled: true\n"
	}
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
`+obs)
	writeFile(t, filepath.Join(apiDir, config.ServiceManifestFileName), serviceManifest)

	ws := &workspace.Workspace{Name: "testws", Path: root, OtelOTLPGRPCPort: 4317}
	return ws, root, apiDir
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("nil result")
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

const basicService = `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`

// A value written where nothing overrides it must take effect, and say so.
func TestServiceEnvSetManifestTakesEffect(t *testing.T) {
	ws, root, apiDir := newTestWorkspace(t, false, basicService)

	res, err := handleServiceEnvSet(ws, root, "", "api", "NAVEXA_API_URL", "http://localhost:8080", "manifest")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	out := resultText(t, res)

	if !strings.Contains(out, "no higher rung overrides it") {
		t.Errorf("expected a plain success, got:\n%s", out)
	}

	m, err := config.LoadServiceManifest(apiDir)
	if err != nil {
		t.Fatalf("LoadServiceManifest: %v", err)
	}
	if got := m.Env.Values["NAVEXA_API_URL"]; got != "http://localhost:8080" {
		t.Errorf("manifest env.values[NAVEXA_API_URL] = %q, want the written value", got)
	}
}

// The lie this slice exists to kill: writing to a rung that a higher rung beats
// must never report bare success — it must name the rung that wins.
func TestServiceEnvSetEnvrcOverriddenByManifestNamesRung(t *testing.T) {
	ws, root, apiDir := newTestWorkspace(t, false, `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
env:
  values:
    K: from_manifest
`)

	res, err := handleServiceEnvSet(ws, root, "", "api", "K", "from_envrc", "envrc")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	out := resultText(t, res)

	if !strings.Contains(out, "will NOT reach") {
		t.Errorf("expected an explicit override warning, got:\n%s", out)
	}
	if !strings.Contains(out, string(config.RungServiceValues)) {
		t.Errorf("expected the winning rung %q to be named, got:\n%s", config.RungServiceValues, out)
	}
	if strings.Contains(out, "no higher rung overrides it") {
		t.Errorf("reported success for a value that cannot take effect:\n%s", out)
	}

	// The write itself still lands.
	data, err := os.ReadFile(filepath.Join(apiDir, config.EnvrcFileName))
	if err != nil {
		t.Fatalf("read .envrc: %v", err)
	}
	if !strings.Contains(string(data), "K=") {
		t.Errorf(".envrc missing the key:\n%s", data)
	}
}

// Rung 6 is devstack-computed and beats the manifest.
func TestServiceEnvSetManifestOverriddenByComputedNamesRung(t *testing.T) {
	ws, root, _ := newTestWorkspace(t, true, basicService)

	res, err := handleServiceEnvSet(ws, root, "", "api", "OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:9999", "manifest")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	out := resultText(t, res)

	if !strings.Contains(out, "will NOT reach") {
		t.Errorf("expected an override warning, got:\n%s", out)
	}
	if !strings.Contains(out, string(config.RungManaged)) {
		t.Errorf("expected %q to be named as the winner, got:\n%s", config.RungManaged, out)
	}
}

// With observability off, devstack computes nothing, so the same write lands.
func TestServiceEnvSetOtelKeyTakesEffectWhenObservabilityOff(t *testing.T) {
	ws, root, _ := newTestWorkspace(t, false, basicService)

	res, err := handleServiceEnvSet(ws, root, "", "api", "OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:9999", "manifest")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if out := resultText(t, res); !strings.Contains(out, "no higher rung overrides it") {
		t.Errorf("expected success when nothing computes this key, got:\n%s", out)
	}
}

// The placement rule: the caller names the rung, devstack never guesses. A
// secret must never land in the git-committed manifest by default.
func TestServiceEnvSetRequiresTarget(t *testing.T) {
	ws, root, apiDir := newTestWorkspace(t, false, basicService)

	res, err := handleServiceEnvSet(ws, root, "", "api", "AUTH0_CLIENT_SECRET", "shhh", "")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error result when target is omitted")
	}
	if out := resultText(t, res); !strings.Contains(out, "target is required") {
		t.Errorf("expected target guidance, got:\n%s", out)
	}

	if _, err := os.Stat(config.ServiceManifestPath(apiDir)); err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	m, err := config.LoadServiceManifest(apiDir)
	if err != nil {
		t.Fatalf("LoadServiceManifest: %v", err)
	}
	if _, ok := m.Env.Values["AUTH0_CLIENT_SECRET"]; ok {
		t.Error("a set with no target wrote a secret into the committed manifest")
	}
}

func TestServiceEnvSetRejectsUnknownTarget(t *testing.T) {
	ws, root, _ := newTestWorkspace(t, false, basicService)

	res, err := handleServiceEnvSet(ws, root, "", "api", "K", "v", "somewhere")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error result for an unknown target")
	}
}

// target=envrc must keep the value out of the manifest entirely: that is the
// whole point of routing secrets there.
func TestServiceEnvSetEnvrcDoesNotTouchManifest(t *testing.T) {
	ws, root, apiDir := newTestWorkspace(t, false, basicService)

	if _, err := handleServiceEnvSet(ws, root, "", "api", "AUTH0_CLIENT_SECRET", "shhh", "envrc"); err != nil {
		t.Fatalf("set: %v", err)
	}

	m, err := config.LoadServiceManifest(apiDir)
	if err != nil {
		t.Fatalf("LoadServiceManifest: %v", err)
	}
	if _, ok := m.Env.Values["AUTH0_CLIENT_SECRET"]; ok {
		t.Error("target=envrc wrote the secret into the committed manifest")
	}

	data, err := os.ReadFile(filepath.Join(apiDir, config.EnvrcFileName))
	if err != nil {
		t.Fatalf("read .envrc: %v", err)
	}
	if !strings.Contains(string(data), "AUTH0_CLIENT_SECRET=") {
		t.Errorf(".envrc missing the key:\n%s", data)
	}
}

// These files hold live tokens: a value must never appear in tool output.
func TestServiceEnvSetNeverEchoesValue(t *testing.T) {
	const secret = "auth0-live-token-do-not-echo"

	for _, target := range []string{"manifest", "envrc"} {
		t.Run(target, func(t *testing.T) {
			ws, root, _ := newTestWorkspace(t, false, basicService)
			res, err := handleServiceEnvSet(ws, root, "", "api", "K", secret, target)
			if err != nil {
				t.Fatalf("set: %v", err)
			}
			if out := resultText(t, res); strings.Contains(out, secret) {
				t.Errorf("tool output leaked the value:\n%s", out)
			}
		})
	}
}

// An overridden write must not echo the winning rung's value either.
func TestServiceEnvSetOverrideWarningNeverEchoesValue(t *testing.T) {
	const existing = "manifest-live-token-do-not-echo"
	ws, root, _ := newTestWorkspace(t, false, `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
env:
  values:
    K: `+existing+`
`)

	res, err := handleServiceEnvSet(ws, root, "", "api", "K", "new", "envrc")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, "will NOT reach") {
		t.Fatalf("expected an override warning, got:\n%s", out)
	}
	if strings.Contains(out, existing) {
		t.Errorf("override warning leaked the winning value:\n%s", out)
	}
}

// .envrc is executed, not line-parsed: an unquoted value with spaces would be
// word-split and silently resolve to something else.
func TestServiceEnvSetEnvrcQuotesValue(t *testing.T) {
	ws, root, _ := newTestWorkspace(t, false, basicService)

	const value = `a b "c" $d`
	if _, err := handleServiceEnvSet(ws, root, "", "api", "K", value, "envrc"); err != nil {
		t.Fatalf("set: %v", err)
	}

	env, err := config.ResolveEnvrc(filepath.Join(root, "repos", "api"))
	if err != nil {
		t.Fatalf("ResolveEnvrc: %v", err)
	}
	if got := env["K"]; got != value {
		t.Errorf("resolved K = %q, want %q", got, value)
	}
}

// runtime.workDir moves the ladder: set must write the .envrc the service reads.
func TestServiceEnvSetEnvrcHonoursWorkDir(t *testing.T) {
	ws, root, apiDir := newTestWorkspace(t, false, `version: 1
service:
  name: api
runtime:
  workDir: sub
  run:
    command: go run .
`)
	if err := os.MkdirAll(filepath.Join(apiDir, "sub"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res, err := handleServiceEnvSet(ws, root, "", "api", "K", "v", "envrc")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.IsError {
		t.Fatalf("set failed: %s", resultText(t, res))
	}

	if _, err := os.Stat(filepath.Join(apiDir, "sub", config.EnvrcFileName)); err != nil {
		t.Errorf("expected .envrc in the service's workDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(apiDir, config.EnvrcFileName)); err == nil {
		t.Error("wrote .envrc at the repo root, which the ladder does not read")
	}
}

func TestServiceEnvSetUnknownService(t *testing.T) {
	ws, root, _ := newTestWorkspace(t, false, basicService)

	res, err := handleServiceEnvSet(ws, root, "", "nope", "K", "v", "manifest")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error result for an unknown service")
	}
}

// newTwoServiceWorkspace lays down "api" and "worker" with the given env.values.
func newTwoServiceWorkspace(t *testing.T, apiValues, workerValues string) (*workspace.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: testws
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
      - ./repos/worker
`)
	for name, values := range map[string]string{"api": apiValues, "worker": workerValues} {
		writeFile(t, filepath.Join(root, "repos", name, config.ServiceManifestFileName), `version: 1
service:
  name: `+name+`
runtime:
  run:
    command: go run .
env:
  values:
`+values)
	}
	return &workspace.Workspace{Name: "testws", Path: root}, root
}

// check still catches a real, per-service defect: a value nobody filled in.
func TestServiceEnvCheckCatchesPlaceholder(t *testing.T) {
	ws, root := newTwoServiceWorkspace(t,
		"    API_TOKEN: TODO\n",
		"    API_TOKEN: real\n")

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, err := handleServiceEnvCheck(ws, root, "", cfg, "api,worker", "")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	out := resultText(t, res)

	if !strings.Contains(out, "placeholder") || !strings.Contains(out, "API_TOKEN") {
		t.Errorf("expected a placeholder finding for API_TOKEN, got:\n%s", out)
	}
}

// Services disagreeing is not a defect: agreement is consensus, not correctness.
// If every service were wrong identically, the old detector reported healthy.
func TestServiceEnvCheckMakesNoConsensusClaim(t *testing.T) {
	ws, root := newTwoServiceWorkspace(t,
		"    MAIN_DATABASE_URL: postgres://localhost:5432/a\n",
		"    MAIN_DATABASE_URL: postgres://localhost:5432/b\n")

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, err := handleServiceEnvCheck(ws, root, "", cfg, "api,worker", "")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	out := resultText(t, res)

	if strings.Contains(out, "MISMATCH") {
		t.Errorf("check still reports cross-service disagreement as a defect:\n%s", out)
	}
	if strings.Contains(out, "MAIN_DATABASE_URL") {
		t.Errorf("check still makes a claim about differing DB URLs:\n%s", out)
	}
}
