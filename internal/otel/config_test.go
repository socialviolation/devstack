package otel

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func openobserveLike(endpoint string) Contribution {
	return Contribution{
		Processors: map[string]any{"batch": map[string]any{"timeout": "2s"}},
		Exporters:  map[string]any{"otlphttp/openobserve": map[string]any{"endpoint": endpoint}},
		Traces:     Pipeline{Processors: []string{"batch"}, Exporters: []string{"otlphttp/openobserve"}},
		Metrics:    Pipeline{Processors: []string{"batch"}, Exporters: []string{"otlphttp/openobserve"}},
		Logs:       Pipeline{Processors: []string{"batch"}, Exporters: []string{"otlphttp/openobserve"}},
	}
}

func parse(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("generated config is not valid YAML: %v\n%s", err, raw)
	}
	return cfg
}

func section(t *testing.T, cfg map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := cfg[key].(map[string]any)
	if !ok {
		t.Fatalf("config has no %q section: %v", key, cfg)
	}
	return v
}

func TestBuildConfigReceiverPorts(t *testing.T) {
	raw, err := BuildConfig(4317, 4318, []WorkspaceContribution{
		{Workspace: "navexa", Plugin: "openobserve", Contribution: openobserveLike("http://localhost:5080/api/default")},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{"0.0.0.0:4317", "0.0.0.0:4318"} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing receiver endpoint %q:\n%s", want, got)
		}
	}
}

func TestBuildConfigSingleShapeSkipsRouting(t *testing.T) {
	raw, err := BuildConfig(4317, 4318, []WorkspaceContribution{
		{Workspace: "navexa", Plugin: "openobserve", Contribution: openobserveLike("http://localhost:5080/api/default")},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, raw)

	if _, ok := cfg["connectors"]; ok {
		t.Errorf("single-backend config should not declare a routing connector:\n%s", raw)
	}
	pipelines := section(t, section(t, cfg, "service"), "pipelines")
	for _, signal := range []string{"traces", "metrics", "logs"} {
		p, ok := pipelines[signal].(map[string]any)
		if !ok {
			t.Fatalf("missing %q pipeline: %v", signal, pipelines)
		}
		if got := p["receivers"]; !equalStrings(got, []string{"otlp"}) {
			t.Errorf("%s pipeline receivers = %v, want [otlp]", signal, got)
		}
		if got := p["exporters"]; !equalStrings(got, []string{"otlphttp/openobserve"}) {
			t.Errorf("%s pipeline exporters = %v", signal, got)
		}
	}
}

// Workspaces on the same backend share one set of pipelines rather than each
// getting a routed copy — the common case stays a plain collector config.
func TestBuildConfigMergesIdenticalWorkspaces(t *testing.T) {
	c := openobserveLike("http://localhost:5080/api/default")
	raw, err := BuildConfig(4317, 4318, []WorkspaceContribution{
		{Workspace: "navexa", Plugin: "openobserve", Contribution: c},
		{Workspace: "roi", Plugin: "openobserve", Contribution: c},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, raw)

	if _, ok := cfg["connectors"]; ok {
		t.Errorf("workspaces on one backend should not be routed:\n%s", raw)
	}
	exporters := section(t, cfg, "exporters")
	if len(exporters) != 1 {
		t.Errorf("exporters = %v, want a single shared exporter", exporters)
	}
}

func TestBuildConfigRoutesDifferingBackends(t *testing.T) {
	forwarding := Contribution{
		Processors: map[string]any{"batch": map[string]any{}},
		Exporters:  map[string]any{"otlphttp": map[string]any{"endpoint": "https://otel.example.com"}},
		Traces:     Pipeline{Processors: []string{"batch"}, Exporters: []string{"otlphttp"}},
	}
	raw, err := BuildConfig(4317, 4318, []WorkspaceContribution{
		{Workspace: "navexa", Plugin: "openobserve", Contribution: openobserveLike("http://localhost:5080/api/default")},
		{Workspace: "roi", Plugin: "forwarding", Contribution: forwarding},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, raw)

	connectors := section(t, cfg, "connectors")
	routing, ok := connectors["routing/traces"].(map[string]any)
	if !ok {
		t.Fatalf("expected a routing/traces connector, got %v", connectors)
	}

	table, ok := routing["table"].([]any)
	if !ok || len(table) != 2 {
		t.Fatalf("routing table = %v, want one entry per workspace", routing["table"])
	}
	conditions := map[string]bool{}
	for _, entry := range table {
		e := entry.(map[string]any)
		conditions[e["condition"].(string)] = true
	}
	for _, want := range []string{
		`attributes["devstack.workspace"] == "navexa"`,
		`attributes["devstack.workspace"] == "roi"`,
	} {
		if !conditions[want] {
			t.Errorf("routing table missing condition %q, got %v", want, conditions)
		}
	}

	pipelines := section(t, section(t, cfg, "service"), "pipelines")
	in, ok := pipelines["traces/in"].(map[string]any)
	if !ok {
		t.Fatalf("missing traces/in pipeline: %v", pipelines)
	}
	if got := in["exporters"]; !equalStrings(got, []string{"routing/traces"}) {
		t.Errorf("traces/in exporters = %v, want [routing/traces]", got)
	}
	for _, name := range []string{"traces/navexa", "traces/roi"} {
		p, ok := pipelines[name].(map[string]any)
		if !ok {
			t.Fatalf("missing %q pipeline: %v", name, pipelines)
		}
		if got := p["receivers"]; !equalStrings(got, []string{"routing/traces"}) {
			t.Errorf("%s receivers = %v, want [routing/traces]", name, got)
		}
	}

	// A workspace only exports metrics/logs in one of the two shapes, so those
	// signals must not silently pick up the other's exporters.
	if _, ok := pipelines["metrics/roi"]; ok {
		t.Errorf("roi contributed no metrics pipeline but one was emitted: %v", pipelines)
	}
}

// Two workspaces forwarding to different upstreams both name their exporter
// "otlphttp"; without renaming, one would overwrite the other.
func TestBuildConfigRenamesCollidingComponents(t *testing.T) {
	upstream := func(endpoint string) Contribution {
		return Contribution{
			Exporters: map[string]any{"otlphttp": map[string]any{"endpoint": endpoint}},
			Traces:    Pipeline{Exporters: []string{"otlphttp"}},
		}
	}
	raw, err := BuildConfig(4317, 4318, []WorkspaceContribution{
		{Workspace: "navexa", Plugin: "forwarding", Contribution: upstream("https://a.example.com")},
		{Workspace: "roi", Plugin: "forwarding", Contribution: upstream("https://b.example.com")},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := parse(t, raw)

	exporters := section(t, cfg, "exporters")
	for _, want := range []string{"otlphttp/navexa", "otlphttp/roi"} {
		if _, ok := exporters[want]; !ok {
			t.Errorf("missing renamed exporter %q, got %v", want, exporters)
		}
	}
	if got := exporters["otlphttp/navexa"].(map[string]any)["endpoint"]; got != "https://a.example.com" {
		t.Errorf("navexa exporter endpoint = %v", got)
	}
	if got := exporters["otlphttp/roi"].(map[string]any)["endpoint"]; got != "https://b.example.com" {
		t.Errorf("roi exporter endpoint = %v", got)
	}
}

func TestBuildConfigNoWorkspaces(t *testing.T) {
	if _, err := BuildConfig(4317, 4318, nil); err == nil {
		t.Error("expected an error when no workspace contributes")
	}
}

func TestQualify(t *testing.T) {
	tests := []struct{ id, suffix, want string }{
		{"otlphttp", "navexa", "otlphttp/navexa"},
		{"otlphttp/openobserve", "navexa", "otlphttp/openobserve_navexa"},
	}
	for _, tt := range tests {
		if got := qualify(tt.id, tt.suffix); got != tt.want {
			t.Errorf("qualify(%q, %q) = %q, want %q", tt.id, tt.suffix, got, tt.want)
		}
	}
}

func equalStrings(got any, want []string) bool {
	list, ok := got.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i, v := range list {
		if v != want[i] {
			return false
		}
	}
	return true
}
