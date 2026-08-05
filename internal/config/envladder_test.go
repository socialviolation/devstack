package config

import (
	"os"
	"path/filepath"
	"testing"
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

// The ladder's whole purpose is its order: each rung must beat the one below it.
func TestEnvLadderPrecedenceOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, EnvrcFileName), "export K=envrc\nexport ONLY_ENVRC=1\n")
	writeFile(t, filepath.Join(dir, "ws.env"), "export K=ws_file\n")
	writeFile(t, filepath.Join(dir, "svc.env"), "export K=svc_file\n")

	ws := &WorkspaceManifest{
		Env: WorkspaceManifestEnv{
			Files:  []string{"ws.env"},
			Values: map[string]string{"K": "ws_values"},
		},
		Environments: map[string]WorkspaceEnvironment{
			"prod": {Values: map[string]string{"K": "active_env"}},
		},
	}
	ws.Workspace.Env = "prod"
	m := &ServiceManifest{Env: ServiceEnv{
		Files:  []string{"svc.env"},
		Values: map[string]string{"K": "svc_values"},
	}}

	layers, err := EnvLadder(dir, ws, m, "", map[string]string{"K": "managed"}, "")
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}

	wantRungs := []EnvRung{RungEnvrc, RungWorkspaceFiles, RungServiceFiles, RungWorkspaceValues, RungServiceValues, RungActiveEnv, RungManaged}
	if len(layers) != len(wantRungs) {
		t.Fatalf("got %d layers, want %d", len(layers), len(wantRungs))
	}
	for i, want := range wantRungs {
		if layers[i].Rung != want {
			t.Errorf("layer %d rung = %q, want %q", i, layers[i].Rung, want)
		}
	}

	env := MergeEnvLadder(layers)
	if got := env["K"]; got != "managed" {
		t.Errorf("K = %q, want %q (devstack-computed must win)", got, "managed")
	}
	if got := env["ONLY_ENVRC"]; got != "1" {
		t.Errorf("ONLY_ENVRC = %q, want 1", got)
	}
}

// Each rung must be beaten by every rung above it, and by none below.
func TestEnvLadderEachRungBeatsTheOneBelow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, EnvrcFileName), "export K=envrc\n")
	writeFile(t, filepath.Join(dir, "svc.env"), "export K=svc_file\n")

	cases := []struct {
		name string
		ws   *WorkspaceManifest
		m    *ServiceManifest
		want string
	}{
		{
			name: "envrc alone",
			ws:   &WorkspaceManifest{},
			m:    &ServiceManifest{},
			want: "envrc",
		},
		{
			name: "service env.files beats .envrc",
			ws:   &WorkspaceManifest{},
			m:    &ServiceManifest{Env: ServiceEnv{Files: []string{"svc.env"}}},
			want: "svc_file",
		},
		{
			name: "workspace env.values beats env.files",
			ws:   &WorkspaceManifest{Env: WorkspaceManifestEnv{Values: map[string]string{"K": "ws_values"}}},
			m:    &ServiceManifest{Env: ServiceEnv{Files: []string{"svc.env"}}},
			want: "ws_values",
		},
		{
			name: "service env.values beats workspace env.values",
			ws:   &WorkspaceManifest{Env: WorkspaceManifestEnv{Values: map[string]string{"K": "ws_values"}}},
			m:    &ServiceManifest{Env: ServiceEnv{Values: map[string]string{"K": "svc_values"}}},
			want: "svc_values",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layers, err := EnvLadder(dir, tc.ws, tc.m, "", nil, "")
			if err != nil {
				t.Fatalf("EnvLadder: %v", err)
			}
			if got := MergeEnvLadder(layers)["K"]; got != tc.want {
				t.Errorf("K = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveEnvPatch(t *testing.T) {
	catalog := map[string]WorkspaceEnvironment{
		"base":  {Values: map[string]string{"K": "base", "ONLY_WS": "w"}},
		"svc":   {Values: map[string]string{"K": "svc"}},
		"stack": {Values: map[string]string{"K": "stack"}},
	}

	cases := []struct {
		name     string
		wsEnv    string
		svcEnv   string
		stackEnv string
		want     map[string]string
		wantErr  string
	}{
		{
			name:  "workspace-only env applies",
			wsEnv: "base",
			want:  map[string]string{"K": "base", "ONLY_WS": "w"},
		},
		{
			name:   "service env overrides workspace for a shared key",
			wsEnv:  "base",
			svcEnv: "svc",
			want:   map[string]string{"K": "svc", "ONLY_WS": "w"},
		},
		{
			name:     "stack env overrides service",
			wsEnv:    "base",
			svcEnv:   "svc",
			stackEnv: "stack",
			want:     map[string]string{"K": "stack", "ONLY_WS": "w"},
		},
		{
			name: "no env active yields empty map",
			want: map[string]string{},
		},
		{
			name:    "unknown workspace env errors",
			wsEnv:   "nope",
			wantErr: `the workspace scope applies environment "nope", and the workspace manifest does not define it`,
		},
		{
			name:    "unknown service env errors",
			svcEnv:  "nope",
			wantErr: `the service scope applies environment "nope", and the workspace manifest does not define it`,
		},
		{
			name:     "unknown stack env errors",
			stackEnv: "nope",
			wantErr:  `the stack scope applies environment "nope", and the workspace manifest does not define it`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := &WorkspaceManifest{Environments: catalog}
			ws.Workspace.Env = tc.wsEnv
			m := &ServiceManifest{}
			m.Service.Env = tc.svcEnv

			got, err := ResolveEnvPatch(ws, m, tc.stackEnv)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveEnvPatch: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("%s = %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

// One directory declares several services, so the service env.values rung must
// name the file that declares THIS service. A caller that reads the rung and
// edits devstack.service.yaml edits a file that is not there.
func TestEnvLadderServiceValuesRungNamesItsOwnManifestFile(t *testing.T) {
	dir := t.TempDir()
	ws := &WorkspaceManifest{}
	m := &ServiceManifest{Env: ServiceEnv{Values: map[string]string{"K": "svc_values"}}}

	layers, err := EnvLadder(dir, ws, m, "", nil, filepath.Join(dir, "devstack.orbit-api.yaml"))
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}

	got := ""
	for _, l := range layers {
		if l.Rung == RungServiceValues {
			got = l.Source
		}
	}
	if got != "devstack.orbit-api.yaml" {
		t.Errorf("service env.values Source = %q, want %q", got, "devstack.orbit-api.yaml")
	}
}

// Some callers build a manifest with no file behind it. They must keep working.
func TestEnvLadderServiceValuesRungFallsBackToTheDefaultFileName(t *testing.T) {
	dir := t.TempDir()
	ws := &WorkspaceManifest{}
	m := &ServiceManifest{Env: ServiceEnv{Values: map[string]string{"K": "svc_values"}}}

	layers, err := EnvLadder(dir, ws, m, "", nil, "")
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}

	got := ""
	for _, l := range layers {
		if l.Rung == RungServiceValues {
			got = l.Source
		}
	}
	if got != ServiceManifestFileName {
		t.Errorf("service env.values Source = %q, want %q", got, ServiceManifestFileName)
	}
}

func TestEnvLadderActiveEnvRung(t *testing.T) {
	dir := t.TempDir()

	ws := &WorkspaceManifest{
		Environments: map[string]WorkspaceEnvironment{
			"prod": {Values: map[string]string{"K": "env_prod", "PORT": "env_port"}},
		},
	}
	ws.Workspace.Env = "prod"
	m := &ServiceManifest{Env: ServiceEnv{Values: map[string]string{"K": "svc_values"}}}

	layers, err := EnvLadder(dir, ws, m, "", map[string]string{"PORT": "managed_port"}, "")
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}

	env := MergeEnvLadder(layers)
	if got := env["K"]; got != "env_prod" {
		t.Errorf("K = %q, want %q (active env must beat service env.values)", got, "env_prod")
	}
	if got := env["PORT"]; got != "managed_port" {
		t.Errorf("PORT = %q, want %q (devstack-computed must beat active env)", got, "managed_port")
	}

	var activeRung EnvLayer
	for _, l := range layers {
		if l.Rung == RungActiveEnv {
			activeRung = l
		}
	}
	if activeRung.Source != "prod" {
		t.Errorf("active env rung Source = %q, want %q (provenance must name which env)", activeRung.Source, "prod")
	}
}

// A stack env that overrides only some of the workspace env's keys must not be
// credited with the keys it never defines.
func TestEnvLadderActiveEnvAttributesKeysToTheEnvThatSuppliedThem(t *testing.T) {
	dir := t.TempDir()

	ws := &WorkspaceManifest{
		Environments: map[string]WorkspaceEnvironment{
			"dev":  {Values: map[string]string{"A": "dev_a", "B": "dev_b"}},
			"perf": {Values: map[string]string{"B": "perf_b"}},
		},
	}
	ws.Workspace.Env = "dev"
	m := &ServiceManifest{}

	layers, err := EnvLadder(dir, ws, m, "perf", nil, "")
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}

	source := map[string]string{}
	for _, l := range layers {
		if l.Rung != RungActiveEnv {
			continue
		}
		for k := range l.Values {
			source[k] = l.Source
		}
	}
	if source["A"] != "dev" {
		t.Errorf("A attributed to env %q, want %q (perf never defines A)", source["A"], "dev")
	}
	if source["B"] != "perf" {
		t.Errorf("B attributed to env %q, want %q", source["B"], "perf")
	}
	if got := MergeEnvLadder(layers)["B"]; got != "perf_b" {
		t.Errorf("B = %q, want %q (stack env must still win)", got, "perf_b")
	}
}

// Splitting the active-env band into one layer per scope must not change the env
// a service actually receives: the merged ladder still ends at ResolveEnvPatch's
// result layered over the static rungs.
func TestEnvLadderActiveEnvSplitPreservesMerge(t *testing.T) {
	dir := t.TempDir()

	ws := &WorkspaceManifest{
		Env: WorkspaceManifestEnv{Values: map[string]string{"A": "ws_values", "D": "ws_values"}},
		Environments: map[string]WorkspaceEnvironment{
			"dev":     {Values: map[string]string{"A": "dev_a", "B": "dev_b", "C": "dev_c"}},
			"staging": {Values: map[string]string{"B": "staging_b", "C": "staging_c"}},
			"perf":    {Values: map[string]string{"C": "perf_c"}},
		},
	}
	ws.Workspace.Env = "dev"
	m := &ServiceManifest{Env: ServiceEnv{Values: map[string]string{"A": "svc_values", "E": "svc_values"}}}
	m.Service.Env = "staging"

	layers, err := EnvLadder(dir, ws, m, "perf", map[string]string{"M": "managed"}, "")
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}

	patch, err := ResolveEnvPatch(ws, m, "perf")
	if err != nil {
		t.Fatalf("ResolveEnvPatch: %v", err)
	}
	want := map[string]string{
		"A": "dev_a",
		"B": "staging_b",
		"C": "perf_c",
		"D": "ws_values",
		"E": "svc_values",
		"M": "managed",
	}
	viaPatch := MergeEnvLadder([]EnvLayer{
		{Values: ws.Env.Values},
		{Values: m.Env.Values},
		{Values: patch},
		{Values: map[string]string{"M": "managed"}},
	})
	for k, v := range want {
		if viaPatch[k] != v {
			t.Fatalf("test premise broken: ResolveEnvPatch over the static layers gives %s = %q, want %q", k, viaPatch[k], v)
		}
	}

	got := MergeEnvLadder(layers)
	if len(got) != len(want) {
		t.Fatalf("merged ladder has %d keys, want %d (%v vs %v)", len(got), len(want), got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestEnvLadderActiveEnvSkipsUnsetScopes(t *testing.T) {
	dir := t.TempDir()

	ws := &WorkspaceManifest{
		Environments: map[string]WorkspaceEnvironment{
			"dev": {Values: map[string]string{"A": "dev_a"}},
		},
	}
	m := &ServiceManifest{}

	layers, err := EnvLadder(dir, ws, m, "", nil, "")
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}
	for _, l := range layers {
		if l.Rung == RungActiveEnv {
			t.Fatalf("no env is applied at any scope, but got an active-env layer %+v", l)
		}
	}

	ws.Workspace.Env = "dev"
	layers, err = EnvLadder(dir, ws, m, "", nil, "")
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}
	n := 0
	for _, l := range layers {
		if l.Rung == RungActiveEnv {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("one scope is set, got %d active-env layers, want 1", n)
	}
}

func TestEnvLadderUndefinedEnvIsAnError(t *testing.T) {
	dir := t.TempDir()

	scopes := []struct {
		name     string
		wsEnv    string
		svcEnv   string
		stackEnv string
	}{
		{"workspace", "ghost", "", ""},
		{"service", "", "ghost", ""},
		{"stack", "", "", "ghost"},
	}
	for _, tc := range scopes {
		t.Run(tc.name, func(t *testing.T) {
			ws := &WorkspaceManifest{Environments: map[string]WorkspaceEnvironment{}}
			ws.Workspace.Env = tc.wsEnv
			m := &ServiceManifest{}
			m.Service.Env = tc.svcEnv

			if _, err := EnvLadder(dir, ws, m, tc.stackEnv, nil, ""); err == nil {
				t.Fatalf("env %q applied at %s scope is not defined, want an error", "ghost", tc.name)
			}
		})
	}
}

func TestOverriderOf(t *testing.T) {
	layers := []EnvLayer{
		{Rung: RungEnvrc, Source: ".envrc", Values: map[string]string{"K": "a", "SECRET": "s"}},
		{Rung: RungServiceValues, Source: ServiceManifestFileName, Values: map[string]string{"K": "b"}},
		{Rung: RungManaged, Values: map[string]string{"OTEL": "x"}},
	}

	if over, ok := OverriderOf(layers, RungEnvrc, "K"); !ok || over.Rung != RungServiceValues {
		t.Errorf("K written at .envrc: got (%q, %v), want service env.values overriding", over.Rung, ok)
	}
	if _, ok := OverriderOf(layers, RungEnvrc, "SECRET"); ok {
		t.Error("SECRET written at .envrc must not be reported as overridden")
	}
	if _, ok := OverriderOf(layers, RungServiceValues, "K"); ok {
		t.Error("K written at service env.values must not be overridden by a rung below it")
	}
	if over, ok := OverriderOf(layers, RungServiceValues, "OTEL"); !ok || over.Rung != RungManaged {
		t.Errorf("OTEL written at service env.values: got (%q, %v), want devstack-computed overriding", over.Rung, ok)
	}
}

// runtime.workDir moves the whole ladder, .envrc included: a writer that ignores
// it writes a file the service never reads.
func TestEnvDirFollowsWorkDir(t *testing.T) {
	svc := ResolvedService{RepoPath: "/repo", Manifest: &ServiceManifest{}}
	if got := svc.EnvDir(); got != "/repo" {
		t.Errorf("EnvDir() = %q, want /repo", got)
	}

	svc.Manifest.Runtime.WorkDir = "sub"
	if got, want := svc.EnvDir(), filepath.Join("/repo", "sub"); got != want {
		t.Errorf("EnvDir() = %q, want %q", got, want)
	}

	svc.Manifest.Runtime.WorkDir = "/elsewhere"
	if got := svc.EnvDir(); got != "/elsewhere" {
		t.Errorf("EnvDir() = %q, want /elsewhere", got)
	}
}
