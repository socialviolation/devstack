package config

import (
	"strings"
	"testing"
)

const templateEnvs = "environments:\n  prod-rag:\n    values:\n      RAG: prod\n"

func TestTemplatesLoadWithServicesGroupsAndEnv(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{"sql": "", "api": "", "web": ""},
		"shared: [sql]\ngroups:\n  front: [web]\n"+templateEnvs+"templates:\n  agent:\n    purpose: Agent work.\n    services: [api, front, sql]\n    env: prod-rag\n")
	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got := rw.Manifest.Templates["agent"]; got.Env != "prod-rag" || len(got.Services) != 3 || got.Purpose != "Agent work." {
		t.Errorf("Templates[agent] = %+v", got)
	}
}

func TestTemplatesRefuseBadManifests(t *testing.T) {
	cases := map[string]struct {
		extra string
		want  string
	}{
		"unknown service": {"templates:\n  agent:\n    services: [ghost]\n", `names "ghost"`},
		"unknown env":     {templateEnvs + "templates:\n  agent:\n    services: [api]\n    env: staging\n", `environment "staging"`},
		"service clash":   {"templates:\n  api:\n    services: [web]\n", "same name as a service"},
		"group clash":     {"groups:\n  front: [web]\ntemplates:\n  front:\n    services: [api]\n", "same name as a group"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeEdgeWorkspace(t, map[string]string{"api": "", "web": ""}, c.extra)
			_, err := ResolveWorkspace(dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ResolveWorkspace = %v, want an error with %q", err, c.want)
			}
		})
	}
}
