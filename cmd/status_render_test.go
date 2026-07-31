package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/tilt"
)

func TestSplitHostResource(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		prefix  string
		wantSvc string
		wantNS  string
		wantOK  bool
	}{
		{"base resource", "navexa:api", "navexa:", "api", "", true},
		{"stack resource", "navexa:api:agent", "navexa:", "api", "agent", true},
		{"other workspace", "other:api", "navexa:", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, ns, ok := splitHostResource(tt.input, tt.prefix)
			if svc != tt.wantSvc || ns != tt.wantNS || ok != tt.wantOK {
				t.Fatalf("splitHostResource(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.input, tt.prefix, svc, ns, ok, tt.wantSvc, tt.wantNS, tt.wantOK)
			}
		})
	}
}

func TestHostResourceMapSelectsNamespace(t *testing.T) {
	resources := []tilt.UIResource{
		resourceNamed("navexa:api"),
		resourceNamed("navexa:api:agent"),
		resourceNamed("navexa:frontend:agent"),
		resourceNamed("other:api:agent"),
	}

	base := hostResourceMap(resources, "navexa", "")
	if len(base) != 1 || base["api"].Metadata.Name != "navexa:api" {
		t.Fatalf("base map = %#v, want only navexa:api under key api", keysOf(base))
	}

	stackMap := hostResourceMap(resources, "navexa", "agent")
	if len(stackMap) != 2 {
		t.Fatalf("stack map keys = %v, want api and frontend", keysOf(stackMap))
	}
	if stackMap["api"].Metadata.Name != "navexa:api:agent" {
		t.Fatalf("stack map api = %q, want navexa:api:agent", stackMap["api"].Metadata.Name)
	}
	if stackMap["frontend"].Metadata.Name != "navexa:frontend:agent" {
		t.Fatalf("stack map frontend = %q, want navexa:frontend:agent", stackMap["frontend"].Metadata.Name)
	}
}

func resourceNamed(name string) tilt.UIResource {
	var r tilt.UIResource
	r.Metadata.Name = name
	return r
}

func keysOf(m map[string]tilt.UIResource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestOtelSegment(t *testing.T) {
	tests := []struct {
		name             string
		running          bool
		enabled          bool
		pluginConfigured bool
		plugin           string
		wantText         string
		wantDecided      bool
	}{
		{
			name:        "running wins",
			running:     true,
			enabled:     true,
			wantText:    "otel ui:http://localhost:5080 otlp:4318 grpc:4317",
			wantDecided: true,
		},
		{
			name:        "enabled but down defers to caller",
			enabled:     true,
			wantText:    "",
			wantDecided: false,
		},
		{
			name:             "configured but not enabled",
			pluginConfigured: true,
			plugin:           "signoz",
			wantText:         "otel: configured (signoz) but not enabled — devstack otel config on",
			wantDecided:      true,
		},
		{
			name:             "plugin config without a plugin name",
			pluginConfigured: true,
			wantText:         "otel: configured (plugin config) but not enabled — devstack otel config on",
			wantDecided:      true,
		},
		{
			name:        "disabled",
			wantText:    "otel: disabled — devstack otel config on",
			wantDecided: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, decided := otelSegment(tt.running, tt.enabled, tt.pluginConfigured, tt.plugin, "http://localhost:5080", 4318, 4317)
			if text != tt.wantText || decided != tt.wantDecided {
				t.Fatalf("otelSegment = (%q, %v), want (%q, %v)", text, decided, tt.wantText, tt.wantDecided)
			}
		})
	}
}

func TestHostOtelLine(t *testing.T) {
	if got := hostOtelLine([]string{"navexa ui:16686 otlp:4318"}, []string{"other"}); got != "otel: navexa ui:16686 otlp:4318" {
		t.Fatalf("running line = %q", got)
	}
	want := "otel: enabled for navexa but collector stopped — devstack otel start"
	if got := hostOtelLine(nil, []string{"navexa"}); got != want {
		t.Fatalf("enabled line = %q, want %q", got, want)
	}
	if got := hostOtelLine(nil, nil); got != "otel: no collector running — devstack otel config on" {
		t.Fatalf("empty line = %q", got)
	}
}

func TestCondenseSection(t *testing.T) {
	tests := []struct {
		running  int
		expand   bool
		erroring bool
		want     bool
	}{
		{running: 0, expand: false, want: true},
		{running: 1, expand: false, want: false},
		{running: 0, expand: true, want: false},
		{running: 3, expand: true, want: false},
		// A failing service is the row worth reading. Collapsing its section
		// answers "the frontend is down" with "nothing is up here" and hides
		// the reason one keystroke away.
		{running: 0, expand: false, erroring: true, want: false},
	}
	for _, tt := range tests {
		if got := condenseSection(tt.running, tt.expand, tt.erroring); got != tt.want {
			t.Fatalf("condenseSection(%d, %v, erroring=%v) = %v, want %v", tt.running, tt.expand, tt.erroring, got, tt.want)
		}
	}
}

// sectionErroring is what decides that, so it has to see a failing member
// through the same status mapping the table uses.
func TestSectionErroringFindsAFailingMember(t *testing.T) {
	s := serviceSection{
		members: []string{"api", "frontend"},
		resources: map[string]tilt.UIResource{
			"api":      {Status: tilt.UIResourceStatus{RuntimeStatus: "ok"}},
			"frontend": {Status: tilt.UIResourceStatus{RuntimeStatus: "error"}},
		},
	}
	if !sectionErroring(s) {
		t.Error("a section with a failing member must not be condensed away")
	}

	delete(s.resources, "frontend")
	if sectionErroring(s) {
		t.Error("no member is failing, so the section may condense")
	}
}

func TestWrapCommaList(t *testing.T) {
	if got := wrapCommaList(nil, 20); got != nil {
		t.Fatalf("wrapCommaList(nil) = %v, want nil", got)
	}

	got := wrapCommaList([]string{"alpha", "beta", "gamma"}, 40)
	if want := []string{"alpha, beta, gamma"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("single line = %v, want %v", got, want)
	}

	names := []string{"alpha", "beta", "gamma", "delta"}
	got = wrapCommaList(names, 14)
	if len(got) < 2 {
		t.Fatalf("expected wrapping, got %v", got)
	}
	for _, line := range got {
		if len(line) > 14 {
			t.Fatalf("line %q exceeds width 14 (all lines: %v)", line, got)
		}
	}
	joined := strings.Join(got, " ")
	for _, n := range names {
		if !strings.Contains(joined, n) {
			t.Fatalf("wrapped output dropped %q: %v", n, got)
		}
	}
}
