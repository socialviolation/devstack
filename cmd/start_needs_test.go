package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func TestServiceStartStartsNeedsNotCallsOrTheOldMap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "dev", "needs")
	svc := func(name, edges string) string {
		return "version: 1\nservice:\n  name: " + name + "\nruntime:\n  run:\n    command: run\n" + edges
	}
	makeStackRepo(t, filepath.Join(root, "api"), svc("api", "needs: [db]\ncalls: [billing]\n"))
	makeStackRepo(t, filepath.Join(root, "db"), svc("db", ""))
	makeStackRepo(t, filepath.Join(root, "billing"), svc("billing", ""))
	makeStackRepo(t, filepath.Join(root, "legacy"), svc("legacy", ""))
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: needs
  repoDiscovery:
    mode: explicit
    repos: [./api, ./db, ./billing, ./legacy]
shared: [api, db, billing, legacy]
dependencies:
  api: [legacy]
`)
	if err := workspace.Register(workspace.Workspace{Name: "needs", Path: root}); err != nil {
		t.Fatal(err)
	}
	serveDaemon(t, `{"uiResources":[{"metadata":{"name":"(Tiltfile)"},"status":{"buildHistory":[{"finishTime":"2999-01-01T00:00:00Z"}]}}]}`)
	t.Setenv("PATH", t.TempDir())

	out := captureStdout(t, func() {
		_ = runEnable(newEnableTestCmd("needs", ""), []string{"api"})
	})
	if !strings.Contains(out, "Starting: db, api\n") {
		t.Errorf("service start must start what api needs, and not what it calls or the old map lists. Got:\n%s", out)
	}
}
