package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFreePortsSpecAcceptsBoolStringAndList(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantAll bool
		wantKey []string
	}{
		{name: "true", yaml: "freePorts: true", wantAll: true},
		{name: "false", yaml: "freePorts: false"},
		{name: "all", yaml: "freePorts: all", wantAll: true},
		{name: "single key", yaml: "freePorts: grpc", wantKey: []string{"grpc"}},
		{name: "list", yaml: "freePorts: [http, grpc]", wantKey: []string{"http", "grpc"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mustWriteFile(t, filepath.Join(dir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
  prep:
    `+tc.yaml+`
ports:
  http: 8080
  grpc: 9090
`)
			m, err := LoadServiceManifest(dir)
			if err != nil {
				t.Fatalf("LoadServiceManifest(): %v", err)
			}
			got := m.Runtime.Prep.FreePorts
			if got.All != tc.wantAll {
				t.Errorf("All = %v, want %v", got.All, tc.wantAll)
			}
			if strings.Join(got.Keys, ",") != strings.Join(tc.wantKey, ",") {
				t.Errorf("Keys = %v, want %v", got.Keys, tc.wantKey)
			}
		})
	}
}

func TestFreePortsResolveAllIsDeterministic(t *testing.T) {
	spec := FreePortsSpec{All: true}
	instance := map[string]int{"http": 8080, "grpc": 9090, "admin": 7070}
	first, err := spec.Resolve("api", instance)
	if err != nil {
		t.Fatalf("Resolve(): %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := spec.Resolve("api", instance)
		if err != nil {
			t.Fatalf("Resolve(): %v", err)
		}
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("Resolve() order is not stable: %v then %v", first, again)
			}
		}
	}
}

func TestFreePortsDisabledResolvesToNothing(t *testing.T) {
	got, err := FreePortsSpec{}.Resolve("api", map[string]int{"http": 8080})
	if err != nil || len(got) != 0 {
		t.Fatalf("Resolve() = %v, %v; want no ports and no error", got, err)
	}
}
