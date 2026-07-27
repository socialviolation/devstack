package otel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGeneratedConfigValidates runs the real collector against the generated
// config. The builder can produce well-formed YAML that otelcol still rejects —
// only the binary can settle whether a config is usable.
func TestGeneratedConfigValidates(t *testing.T) {
	bin, err := exec.LookPath("otelcol-contrib")
	if err != nil {
		t.Skip("otelcol-contrib not installed")
	}

	forwarding := Contribution{
		Processors: map[string]any{"batch": map[string]any{}},
		Exporters:  map[string]any{"otlphttp": map[string]any{"endpoint": "https://otel.example.com"}},
		Traces:     Pipeline{Processors: []string{"batch"}, Exporters: []string{"otlphttp"}},
		Metrics:    Pipeline{Processors: []string{"batch"}, Exporters: []string{"otlphttp"}},
		Logs:       Pipeline{Processors: []string{"batch"}, Exporters: []string{"otlphttp"}},
	}

	tests := []struct {
		name     string
		contribs []WorkspaceContribution
	}{
		{
			name: "single backend",
			contribs: []WorkspaceContribution{
				{Workspace: "navexa", Plugin: "openobserve", Contribution: openobserveLike("http://localhost:5080/api/default")},
			},
		},
		{
			name: "routed backends",
			contribs: []WorkspaceContribution{
				{Workspace: "navexa", Plugin: "openobserve", Contribution: openobserveLike("http://localhost:5080/api/default")},
				{Workspace: "roi", Plugin: "forwarding", Contribution: forwarding},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := BuildConfig(4317, 4318, tt.contribs)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, raw, 0644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(bin, "validate", "--config="+path).CombinedOutput()
			if err != nil {
				t.Fatalf("otelcol rejected the generated config: %v\n%s\n--- config ---\n%s", err, out, raw)
			}
		})
	}
}
