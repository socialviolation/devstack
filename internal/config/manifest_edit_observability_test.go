package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const observabilityTestManifest = `version: 1
workspace:
  name: test-ws
  # keep me
  repoDiscovery:
    mode: scan
    roots: [.]
observability:
  enabled: true
  backend: forwarding
  settings:
    upstream: https://otel.example.com:4318
`

func writeObservabilityManifest(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, WorkspaceManifestFileName), []byte(observabilityTestManifest), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSetObservabilitySettingsRoundTrips(t *testing.T) {
	dir := t.TempDir()
	writeObservabilityManifest(t, dir)

	if err := SetObservabilitySettings(dir, map[string]string{
		"upstream":            "https://otel.internal:4317",
		"protocol":            "grpc",
		"resource_attributes": "engineer=test,team=platform",
	}); err != nil {
		t.Fatalf("SetObservabilitySettings: %v", err)
	}

	obs := WorkspaceObservability(dir)
	want := map[string]string{
		"upstream":            "https://otel.internal:4317",
		"protocol":            "grpc",
		"resource_attributes": "engineer=test,team=platform",
	}
	for k, v := range want {
		if obs.Settings[k] != v {
			t.Errorf("settings[%q] = %q, want %q", k, obs.Settings[k], v)
		}
	}
	if obs.Backend != "forwarding" {
		t.Errorf("backend = %q, want %q", obs.Backend, "forwarding")
	}

	raw, err := os.ReadFile(filepath.Join(dir, WorkspaceManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# keep me") {
		t.Errorf("comment lost on round-trip:\n%s", raw)
	}
}

func TestSetObservabilitySettingsCreatesBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, WorkspaceManifestFileName),
		[]byte("version: 1\nworkspace:\n  name: bare-ws\n  repoDiscovery:\n    mode: scan\n    roots: [.]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetObservabilitySettings(dir, map[string]string{"upstream": "otel.example.com:4317"}); err != nil {
		t.Fatalf("SetObservabilitySettings: %v", err)
	}
	if got := WorkspaceObservability(dir).Settings["upstream"]; got != "otel.example.com:4317" {
		t.Errorf("upstream = %q, want %q", got, "otel.example.com:4317")
	}
}

func TestSetObservabilitySettingsRefusesCredentials(t *testing.T) {
	for _, key := range []string{"api_key", "auth_token", "password", "client_secret"} {
		dir := t.TempDir()
		writeObservabilityManifest(t, dir)

		err := SetObservabilitySettings(dir, map[string]string{key: "synthetic-value"})
		if err == nil {
			t.Fatalf("SetObservabilitySettings(%q) succeeded, want refusal", key)
		}
		if !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), ".envrc") {
			t.Errorf("error for %q is not actionable: %v", key, err)
		}

		raw, readErr := os.ReadFile(filepath.Join(dir, WorkspaceManifestFileName))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(raw), "synthetic-value") {
			t.Errorf("credential %q was written to the manifest:\n%s", key, raw)
		}
	}
}

func TestSetObservabilitySettingsRefusesWholeBatchOnCredential(t *testing.T) {
	dir := t.TempDir()
	writeObservabilityManifest(t, dir)

	err := SetObservabilitySettings(dir, map[string]string{
		"protocol": "grpc",
		"api_key":  "synthetic-value",
	})
	if err == nil {
		t.Fatal("mixed batch succeeded, want refusal")
	}
	if got := WorkspaceObservability(dir).Settings["protocol"]; got == "grpc" {
		t.Error("non-credential key from a refused batch was persisted")
	}
}
