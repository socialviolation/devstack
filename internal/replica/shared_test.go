package replica

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func newUnsharedTemplate(t *testing.T, shared string) *workspace.Workspace {
	t.Helper()
	root := filepath.Join(t.TempDir(), "dev", "navexa")
	makeRepo(t, filepath.Join(root, "backend"), backendSvc)
	makeRepo(t, filepath.Join(root, "frontend"), frontendSvc)
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos: [./backend, ./frontend]
`+shared)
	return &workspace.Workspace{Name: "navexa", Path: root}
}

func TestEnsureBuildsNoReplicaWithoutSharedServices(t *testing.T) {
	ws := newUnsharedTemplate(t, "")
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(Root(ws)); !os.IsNotExist(err) {
		t.Errorf("Ensure built %s for a workspace with no shared services", Root(ws))
	}
	res, err := Sync(ws)
	if err != nil {
		t.Fatalf("Sync with no shared services: %v", err)
	}
	if len(res.Services) != 0 {
		t.Errorf("Sync reported %v, want nothing", res.Services)
	}
}

func TestEnsureCutsWorktreesOnlyForSharedRepositories(t *testing.T) {
	ws := newUnsharedTemplate(t, "shared: [backend]\n")
	res, err := Ensure(ws)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := services(res.Created); !reflect.DeepEqual(got, []string{"backend"}) {
		t.Errorf("Created = %v, want only the shared repository", got)
	}
	rw, err := Resolve(ws)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := rw.Services["frontend"]; ok {
		t.Error("the replica manifest lists frontend, which is not shared")
	}
	if !rw.IsShared("backend") {
		t.Error("Resolve dropped the shared list of the template")
	}
}

func TestSyncSharedRestartsOnlyTheServicesWhoseRepositoryMoved(t *testing.T) {
	ws := newUnsharedTemplate(t, "shared: [backend, frontend]\n")
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	backend := filepath.Join(ws.Path, "backend")
	writeFile(t, filepath.Join(backend, "new.txt"), "x\n")
	rungit(t, backend, "add", "-f", ".")
	rungit(t, backend, "commit", "-q", "-m", "move")

	var calls []string
	res, err := SyncShared(ws, func(svc string) (bool, error) {
		calls = append(calls, svc)
		return true, nil
	})
	if err != nil {
		t.Fatalf("SyncShared: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"backend"}) {
		t.Errorf("restart calls = %v, want only backend", calls)
	}
	if !reflect.DeepEqual(res.Restarted, []string{"backend"}) {
		t.Errorf("Restarted = %v, want [backend]", res.Restarted)
	}
	if _, err := os.Stat(filepath.Join(Root(ws), "backend", "new.txt")); err != nil {
		t.Errorf("the replica did not move to the new tip: %v", err)
	}
}

func TestSyncSharedReportsARestartThatFailsAndKeepsGoing(t *testing.T) {
	ws := newUnsharedTemplate(t, "shared: [backend, frontend]\n")
	if _, err := Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, repo := range []string{"backend", "frontend"} {
		dir := filepath.Join(ws.Path, repo)
		writeFile(t, filepath.Join(dir, "new.txt"), "x\n")
		rungit(t, dir, "add", "-f", ".")
		rungit(t, dir, "commit", "-q", "-m", "move")
	}
	res, err := SyncShared(ws, func(svc string) (bool, error) {
		if svc == "backend" {
			return false, errors.New("boom")
		}
		return svc == "frontend", nil
	})
	if err == nil {
		t.Fatal("SyncShared = nil error, want the failed restart")
	}
	if res == nil || !reflect.DeepEqual(res.Restarted, []string{"frontend"}) {
		t.Errorf("Restarted = %+v, want [frontend]", res)
	}
}

func TestLoadRefusesASharedNameThatIsNotAService(t *testing.T) {
	ws := newUnsharedTemplate(t, "shared: [ghost]\n")
	if _, err := config.ResolveWorkspace(ws.Path); err == nil {
		t.Fatal("ResolveWorkspace accepted a shared name that is not a service")
	}
}
