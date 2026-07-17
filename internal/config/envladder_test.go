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

	ws := &WorkspaceManifest{Env: WorkspaceManifestEnv{
		Files:  []string{"ws.env"},
		Values: map[string]string{"K": "ws_values"},
	}}
	m := &ServiceManifest{Env: ServiceEnv{
		Files:  []string{"svc.env"},
		Values: map[string]string{"K": "svc_values"},
	}}

	layers, err := EnvLadder(dir, ws, m, map[string]string{"K": "managed"})
	if err != nil {
		t.Fatalf("EnvLadder: %v", err)
	}

	wantRungs := []EnvRung{RungEnvrc, RungWorkspaceFiles, RungServiceFiles, RungWorkspaceValues, RungServiceValues, RungManaged}
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
			layers, err := EnvLadder(dir, tc.ws, tc.m, nil)
			if err != nil {
				t.Fatalf("EnvLadder: %v", err)
			}
			if got := MergeEnvLadder(layers)["K"]; got != tc.want {
				t.Errorf("K = %q, want %q", got, tc.want)
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
