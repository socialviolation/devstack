package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddServiceEditsMergesAndPromotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ServiceManifestFileName)
	if err := os.WriteFile(path, []byte(`version: 1
service:
  name: api
# keep me
runtime:
  run:
    command: go run .
calls:
  - web
  - service: queue
    via: servicebus
`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := AddServiceEdges(path, []string{"web", "mail"}, []string{"queue"}); err != nil {
		t.Fatal(err)
	}

	m, err := LoadServiceManifestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, e := range m.Calls {
		calls = append(calls, e.Service)
	}
	if strings.Join(calls, ",") != "web,mail" {
		t.Errorf("calls = %v, want [web mail]", calls)
	}
	if len(m.Needs) != 1 || m.Needs[0].Service != "queue" || m.Needs[0].Via != "servicebus" {
		t.Errorf("needs = %+v, want queue with its via kept", m.Needs)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# keep me") {
		t.Errorf("the comment is gone:\n%s", data)
	}
}

func TestRemoveLegacyEdgeMapsKeepsTheCommentOfTheNextKey(t *testing.T) {
	for name, gap := range map[string]string{"no blank line": "", "blank line": "\n"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "version: 2\nworkspace:\n  name: w\n  repoDiscovery:\n    repos: [./a]\n"+
				"# start order\ndependencies:\n  a: [b]\n  c: [d]\n# hooks run on events\n"+gap+"environments:\n  dev: {}\n")

			if err := RemoveLegacyEdgeMaps(dir); err != nil {
				t.Fatal(err)
			}

			out := readManifest(t, dir)
			if !strings.Contains(out, "# hooks run on events\nenvironments:") {
				t.Errorf("the comment above environments is gone:\n%s", out)
			}
			if strings.Contains(out, "dependencies") || strings.Contains(out, "# start order") {
				t.Errorf("the dependencies map or its comment is still there:\n%s", out)
			}
		})
	}
}

func TestSetSharedGoesAboveGroups(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "version: 2\nworkspace:\n  name: w\n  repoDiscovery:\n    repos: [./a]\ngroups:\n  core: [a]\n")

	if err := SetShared(dir, []string{"db", "cache"}); err != nil {
		t.Fatal(err)
	}

	out := readManifest(t, dir)
	if !strings.Contains(out, "shared: [db, cache]\ngroups:") {
		t.Errorf("shared is not above groups:\n%s", out)
	}
}
