package replica

import (
	"path/filepath"
	"strings"
	"testing"
)

// A config edit reaches base through Resolve, before any sync, because the
// checkout is the source of truth for every one of these fields.
func TestResolveTakesTheConfigurationFromTheTemplate(t *testing.T) {
	ws := newTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	writeFile(t, filepath.Join(ws.Path, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  env: dev
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./frontend
environments:
  dev:
    values:
      TIER: dev
env:
  values:
    REGION: au
groups:
  web: [frontend]
dependencies:
  frontend: [backend]
calls:
  frontend: [backend]
startsAfter:
  frontend: [backend]
`)

	rw, err := Resolve(ws)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := rw.Manifest.Dependencies["frontend"]; len(got) != 1 || got[0] != "backend" {
		t.Errorf("Dependencies = %v, want the template's frontend -> backend", rw.Manifest.Dependencies)
	}
	if got := rw.Manifest.Groups["web"]; len(got) != 1 || got[0] != "frontend" {
		t.Errorf("Groups = %v, want the template's web group", rw.Manifest.Groups)
	}
	if got := rw.Manifest.Calls["frontend"]; len(got) != 1 || got[0] != "backend" {
		t.Errorf("Calls = %v, want the template's frontend -> backend", rw.Manifest.Calls)
	}
	if got := rw.Manifest.StartsAfter["frontend"]; len(got) != 1 || got[0] != "backend" {
		t.Errorf("StartsAfter = %v, want the template's frontend -> backend", rw.Manifest.StartsAfter)
	}
	if got := rw.Manifest.Env.Values["REGION"]; got != "au" {
		t.Errorf("Env.Values[REGION] = %q, want au", got)
	}
}

// A sync generates the replica manifest again. Without that, base keeps the
// configuration of the last build.
func TestSyncWritesTheManifestAgain(t *testing.T) {
	ws := newTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	before := readFile(t, filepath.Join(Root(ws), "devstack.workspace.yaml"))
	if strings.Contains(before, "dependencies") {
		t.Fatalf("the replica manifest already declares a dependency:\n%s", before)
	}

	writeWorkspaceManifestWith(t, ws.Path, "dependencies:\n  frontend: [backend]\n", "backend", "frontend")
	if _, err := Sync(ws); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	after := readFile(t, filepath.Join(Root(ws), "devstack.workspace.yaml"))
	if !strings.Contains(after, "dependencies") {
		t.Fatalf("the sync did not write the manifest again, so the dependency never reached base:\n%s", after)
	}
}

// A repository added to the workspace after the replica was built has no
// worktree. A sync builds it, rather than telling the reader to run a command
// they have just run.
func TestSyncBuildsTheWorktreeOfARepositoryAddedSinceTheBuild(t *testing.T) {
	ws := newTemplate(t)
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	makeRepo(t, filepath.Join(ws.Path, "worker"), workerSvc)
	writeWorkspaceManifest(t, ws.Path, "backend", "frontend", "worker")

	res, err := Sync(ws)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "no worktree") {
			t.Fatalf("Sync warned instead of building the worktree: %s", w)
		}
	}
	var built bool
	for _, wt := range res.Created {
		if wt.Repo == "worker" {
			built = true
		}
	}
	if !built {
		t.Fatalf("Created = %v, want the worker worktree", services(res.Created))
	}
	var synced bool
	for _, s := range res.Services {
		if s.Service == "worker" {
			synced = true
		}
	}
	if !synced {
		t.Fatalf("Services = %v, want worker among them", res.Services)
	}
}

func writeWorkspaceManifestWith(t *testing.T, root, extra string, repos ...string) {
	t.Helper()
	manifest := `version: 1
workspace:
  name: navexa
  env: dev
  repoDiscovery:
    mode: explicit
    repos:
`
	for _, r := range repos {
		manifest += "      - ./" + r + "\n"
	}
	manifest += `environments:
  dev:
    values:
      TIER: dev
`
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), manifest+extra)
}
