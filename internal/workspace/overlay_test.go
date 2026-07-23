package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const overlayManifest = `version: 1
workspace:
  name: overlay-ws
  repoDiscovery:
    mode: scan
    roots: [.]
observability:
  enabled: true
  backend: forwarding
  settings:
    upstream: https://manifest.example.com:4318
    protocol: grpc
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := warnWriter
	warnWriter = buf
	t.Cleanup(func() { warnWriter = prev })
	return buf
}

func TestOverlayProjectConfigTakesManifestSettings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devstack.workspace.yaml"), overlayManifest)

	ws := &Workspace{
		Name:             "overlay-ws",
		Path:             dir,
		OtelPlugin:       "signoz",
		OtelPluginConfig: map[string]string{"upstream": "https://registry.example.com:4318", "api_key": "synthetic"},
	}
	ws.OverlayProjectConfig()

	if ws.OtelPlugin != "forwarding" {
		t.Errorf("OtelPlugin = %q, want %q", ws.OtelPlugin, "forwarding")
	}
	if got := ws.PluginConfig("upstream"); got != "https://manifest.example.com:4318" {
		t.Errorf("manifest upstream did not win: %q", got)
	}
	if got := ws.PluginConfig("protocol"); got != "grpc" {
		t.Errorf("protocol = %q, want grpc", got)
	}
	if got := ws.PluginConfig("api_key"); got != "synthetic" {
		t.Errorf("registry-only key was dropped: %q", got)
	}
}

func TestOverlayProjectConfigWarnsAboutStrandedLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devstack.workspace.yaml"), overlayManifest)
	writeFile(t, filepath.Join(dir, ".devstack.json"), `{
  "otel_plugin": "forwarding",
  "otel_plugin_config": {
    "upstream": "https://stranded.example.com:4318",
    "api_key": "synthetic-secret"
  }
}`)

	buf := captureWarnings(t)
	ws := &Workspace{Name: "overlay-ws", Path: dir}
	ws.OverlayProjectConfig()

	out := buf.String()
	if !strings.Contains(out, ".devstack.json") {
		t.Errorf("warning does not name the legacy file:\n%s", out)
	}
	if !strings.Contains(out, "devstack otel configure --set upstream=https://stranded.example.com:4318") {
		t.Errorf("warning does not name the command that re-applies the setting:\n%s", out)
	}
	if !strings.Contains(out, "api_key") || strings.Contains(out, "synthetic-secret") {
		t.Errorf("credential must be named but never printed:\n%s", out)
	}

	// The stranded values must not be adopted as live config.
	if got := ws.PluginConfig("upstream"); got != "https://manifest.example.com:4318" {
		t.Errorf("legacy store leaked into runtime config: %q", got)
	}

	buf.Reset()
	ws.OverlayProjectConfig()
	if buf.Len() != 0 {
		t.Errorf("warning repeated for the same workspace:\n%s", buf.String())
	}
}

func TestOverlayProjectConfigSilentWhenLegacyMatchesManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "devstack.workspace.yaml"), overlayManifest)
	writeFile(t, filepath.Join(dir, ".devstack.json"), `{
  "otel_plugin": "forwarding",
  "otel_plugin_config": {
    "upstream": "https://manifest.example.com:4318",
    "protocol": "grpc"
  }
}`)

	buf := captureWarnings(t)
	ws := &Workspace{Name: "overlay-ws", Path: dir}
	ws.OverlayProjectConfig()

	if buf.Len() != 0 {
		t.Errorf("warned about a legacy store the manifest already carries:\n%s", buf.String())
	}
}
