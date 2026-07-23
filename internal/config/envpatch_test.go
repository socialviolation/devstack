package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestActiveEnvName(t *testing.T) {
	tests := []struct {
		name     string
		wsEnv    string
		svcEnv   string
		stackEnv string
		want     string
	}{
		{"stack wins", "dev", "staging", "prod", "prod"},
		{"service beats workspace", "dev", "staging", "", "staging"},
		{"workspace fallback", "dev", "", "", "dev"},
		{"all empty", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActiveEnvName(tc.wsEnv, tc.svcEnv, tc.stackEnv); got != tc.want {
				t.Fatalf("ActiveEnvName(%q, %q, %q) = %q, want %q", tc.wsEnv, tc.svcEnv, tc.stackEnv, got, tc.want)
			}
		})
	}
}

// A manifest whose environment still carries legacy type/observability keys must
// contribute exactly its values to the ladder — the inert keys must not appear.
func TestActiveEnvLayersFromLegacyManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  env: remoteprod
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
environments:
  remoteprod:
    type: remote
    observability:
      backend: signoz
      url: https://signoz.example
    values:
      API_URL: https://api.example
`)

	ws, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}

	layers, err := ActiveEnvLayers(ws, &ServiceManifest{}, "")
	if err != nil {
		t.Fatalf("ActiveEnvLayers: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("got %d layers, want 1", len(layers))
	}
	want := map[string]string{"API_URL": "https://api.example"}
	if !reflect.DeepEqual(layers[0].Values, want) {
		t.Errorf("layer values = %v, want %v", layers[0].Values, want)
	}
}
