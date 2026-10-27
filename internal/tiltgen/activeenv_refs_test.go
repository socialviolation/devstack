package tiltgen

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func TestActiveEnvRefsResolveAndKeepTheirPrecedence(t *testing.T) {
	book := config.PortBook{"api": {"http": 20011}}
	layers := []config.EnvLayer{
		{Rung: config.RungWorkspaceValues, Values: map[string]string{"API_URL": "${api.url}/old"}},
		{Rung: config.RungActiveEnv, Source: "perf", Values: map[string]string{"API_URL": "${api.url}/v1"}},
	}
	if err := ResolveLayerRefs(layers, "web", book); err != nil {
		t.Fatalf("ResolveLayerRefs: %v", err)
	}
	if got := config.MergeEnvLadder(layers)["API_URL"]; got != "http://localhost:20011/v1" {
		t.Errorf("API_URL = %q, want the active env's value with its ref resolved", got)
	}
}

func TestUnresolvableActiveEnvRefNamesEnvKeyAndRef(t *testing.T) {
	layers := []config.EnvLayer{
		{Rung: config.RungActiveEnv, Source: "perf", Values: map[string]string{"API_URL": "${ghost.url}"}},
	}
	err := ResolveLayerRefs(layers, "web", config.PortBook{})
	if err == nil {
		t.Fatal("ResolveLayerRefs() = nil, want an error")
	}
	for _, want := range []string{`env "perf"`, "API_URL", "${ghost.url}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}
