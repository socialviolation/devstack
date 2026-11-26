package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
)

func TestScaffoldWorkspaceManifestHasNoEdgeMapAndNoBase(t *testing.T) {
	dir := t.TempDir()
	if _, err := scaffoldWorkspaceManifest(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config.WorkspaceManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"\ndependencies:", "--stack base", "startsAfter"} {
		if strings.Contains(string(data), banned) {
			t.Errorf("workspace scaffold contains %q:\n%s", banned, data)
		}
	}
	if !strings.Contains(string(data), "\nshared: []") {
		t.Errorf("workspace scaffold must declare shared: []:\n%s", data)
	}
	var m config.WorkspaceManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("workspace scaffold does not parse: %v", err)
	}
	if len(m.Dependencies)+len(m.Calls)+len(m.StartsAfter) != 0 {
		t.Errorf("workspace scaffold declares an old edge map: %+v", m)
	}
}

func TestScaffoldServiceManifestDeclaresNeedsAndCalls(t *testing.T) {
	dir := t.TempDir()
	path, err := scaffoldServiceManifest(dir, "api", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nneeds: []", "\ncalls: []"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("service scaffold lacks %q:\n%s", want, data)
		}
	}

	filled := strings.Replace(string(data), `command: ""                 # REQUIRED: the long command that serves this service`, `command: "go run ."`, 1)
	filled = strings.Replace(filled, "needs: []\n  # - my-db\n  # - service: my-api\n  #   via: http", "needs:\n  - my-db\n  - service: my-api\n    via: http", 1)
	filled = strings.Replace(filled, "calls: []\n  # - service: my-worker\n  #   via: queue", "calls:\n  - service: my-worker\n    via: queue", 1)
	filled = strings.Replace(filled, "# migrations: db/migrations", "migrations: db/migrations", 1)
	filled = strings.Replace(filled, "# tasks:\n#   test:\n#     run: go test ./...\n#   seed:\n#     run: ./scripts/seed.sh\n#     timeout: 5m\n#     env:\n#       API_URL: ${self.url}",
		"tasks:\n  test:\n    run: go test ./...\n  seed:\n    run: ./scripts/seed.sh\n    timeout: 5m\n    env:\n      API_URL: ${self.url}", 1)
	target := filepath.Join(dir, "filled.yaml")
	if err := os.WriteFile(target, []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := config.LoadServiceManifestFile(target)
	if err != nil {
		t.Fatalf("service scaffold with its examples does not load: %v", err)
	}
	if len(m.Needs) != 2 || m.Needs[1].Service != "my-api" || m.Needs[1].Via != "http" {
		t.Errorf("needs = %+v, want my-db and my-api via http", m.Needs)
	}
	if len(m.Calls) != 1 || m.Calls[0].Service != "my-worker" || m.Calls[0].Via != "queue" {
		t.Errorf("calls = %+v, want my-worker via queue", m.Calls)
	}
	if m.Migrations != "db/migrations" {
		t.Errorf("migrations = %q, want db/migrations", m.Migrations)
	}
	if m.Tasks["test"].Run != "go test ./..." || m.Tasks["seed"].Timeout != "5m" || m.Tasks["seed"].Env["API_URL"] != "${self.url}" {
		t.Errorf("tasks = %+v, want test and seed as the scaffold shows them", m.Tasks)
	}
}
