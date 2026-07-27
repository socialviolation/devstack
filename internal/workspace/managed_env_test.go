package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeObservabilityWorkspace(t *testing.T, enabled bool) string {
	t.Helper()
	dir := t.TempDir()
	obs := "  enabled: false\n"
	if enabled {
		obs = "  enabled: true\n"
	}
	manifest := "version: 1\n" +
		"workspace:\n" +
		"  name: navexa\n" +
		"  repoDiscovery:\n" +
		"    mode: scan\n" +
		"    roots: [\".\"]\n" +
		"observability:\n" + obs
	if err := os.WriteFile(filepath.Join(dir, "devstack.workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func TestManagedEnvFor_StackAttributes(t *testing.T) {
	dir := writeObservabilityWorkspace(t, true)
	ws := &Workspace{Name: "navexa", Path: dir}

	cases := []struct {
		name      string
		stack     string
		wantStack string
	}{
		{name: "base when empty", stack: "", wantStack: "base"},
		{name: "explicit stack", stack: "perf", wantStack: "perf"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ManagedEnvFor(ws, []string{"api"}, tc.stack, nil)
			svc, ok := got["api"]
			if !ok {
				t.Fatalf("no env for service api, got %v", got)
			}
			if svc["OTEL_SERVICE_NAME"] != "api" {
				t.Errorf("OTEL_SERVICE_NAME = %q, want api", svc["OTEL_SERVICE_NAME"])
			}
			wantAttrs := "devstack.workspace=navexa,devstack.service=api,devstack.stack=" + tc.wantStack
			if svc["OTEL_RESOURCE_ATTRIBUTES"] != wantAttrs {
				t.Errorf("OTEL_RESOURCE_ATTRIBUTES = %q, want %q", svc["OTEL_RESOURCE_ATTRIBUTES"], wantAttrs)
			}
			if svc["OTEL_EXPORTER_OTLP_PROTOCOL"] != "grpc" {
				t.Errorf("OTEL_EXPORTER_OTLP_PROTOCOL = %q, want grpc", svc["OTEL_EXPORTER_OTLP_PROTOCOL"])
			}
			if svc["OTEL_EXPORTER_OTLP_ENDPOINT"] == "" {
				t.Error("OTEL_EXPORTER_OTLP_ENDPOINT is empty")
			}
		})
	}
}

// Every variant of a service reports to the same backend, so the env it runs
// under has to travel with its telemetry to be distinguishable.
func TestManagedEnvFor_EnvAttributes(t *testing.T) {
	dir := writeObservabilityWorkspace(t, true)
	ws := &Workspace{Name: "navexa", Path: dir}

	got := ManagedEnvFor(ws, []string{"api"}, "perf", map[string]string{"api": "perf-env"})
	want := "devstack.workspace=navexa,devstack.service=api,devstack.stack=perf,devstack.env=perf-env"
	if got["api"]["OTEL_RESOURCE_ATTRIBUTES"] != want {
		t.Errorf("OTEL_RESOURCE_ATTRIBUTES = %q, want %q", got["api"]["OTEL_RESOURCE_ATTRIBUTES"], want)
	}

	// deployment.environment belongs to the destination's owner (the forwarding
	// plugin sets it per workspace); devstack must not compete for a key that
	// upstream dashboards group by.
	if strings.Contains(got["api"]["OTEL_RESOURCE_ATTRIBUTES"], "deployment.environment") {
		t.Errorf("devstack must not emit deployment.environment: %q", got["api"]["OTEL_RESOURCE_ATTRIBUTES"])
	}

	// A service with no env selected must not gain an empty attribute.
	got = ManagedEnvFor(ws, []string{"api"}, "", map[string]string{"other": "dev"})
	if strings.Contains(got["api"]["OTEL_RESOURCE_ATTRIBUTES"], "devstack.env") {
		t.Errorf("unexpected env attribute: %q", got["api"]["OTEL_RESOURCE_ATTRIBUTES"])
	}
}

func TestManagedEnv_DelegatesToBase(t *testing.T) {
	dir := writeObservabilityWorkspace(t, true)
	ws := &Workspace{Name: "navexa", Path: dir}

	got := ManagedEnv(ws, []string{"api"}, nil)
	want := "devstack.workspace=navexa,devstack.service=api,devstack.stack=base"
	if got["api"]["OTEL_RESOURCE_ATTRIBUTES"] != want {
		t.Errorf("OTEL_RESOURCE_ATTRIBUTES = %q, want %q", got["api"]["OTEL_RESOURCE_ATTRIBUTES"], want)
	}
}

func TestManagedEnvFor_DisabledIsEmpty(t *testing.T) {
	dir := writeObservabilityWorkspace(t, false)
	ws := &Workspace{Name: "navexa", Path: dir}

	if got := ManagedEnvFor(ws, []string{"api"}, "perf", nil); len(got) != 0 {
		t.Errorf("expected empty map when observability disabled, got %v", got)
	}
	if got := ManagedEnvFor(nil, []string{"api"}, "", nil); len(got) != 0 {
		t.Errorf("expected empty map for nil workspace, got %v", got)
	}
}
