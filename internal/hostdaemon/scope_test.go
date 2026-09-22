package hostdaemon

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func hostTiltfile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(workspace.HostTiltDir(), "Tiltfile"))
	if err != nil {
		t.Fatalf("read the host Tiltfile: %v", err)
	}
	return string(b)
}

func setServicePort(t *testing.T, ws *workspace.Workspace, service string, port int) {
	t.Helper()
	writeFile(t, filepath.Join(ws.Path, service, "devstack.service.yaml"),
		"version: 1\nservice:\n  name: "+service+"\nruntime:\n  run:\n    command: go run .\nports:\n  http: "+strconv.Itoa(port)+"\n")
}

func setSouthfoundryRepos(t *testing.T, ws *workspace.Workspace, repos ...string) {
	t.Helper()
	manifest := "version: 1\nworkspace:\n  name: " + ws.Name + "\n  repoDiscovery:\n    mode: explicit\n    repos:\n"
	for _, r := range repos {
		manifest += "      - ./" + r + "\n"
	}
	writeFile(t, filepath.Join(ws.Path, "devstack.workspace.yaml"), manifest)
}

func seededWorkspaces(t *testing.T) (*workspace.Workspace, *workspace.Workspace) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := newActiveWorkspace(t, home, "navexa", "backend", 8080)
	other := newActiveWorkspace(t, home, "southfoundry", "api", 8090)
	if _, err := SyncScope(ScopeAll()); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	return target, other
}

func TestSyncScopeKeepsAnUntargetedBlockOnDisk(t *testing.T) {
	_, other := seededWorkspaces(t)
	before := resourceBlocks(hostTiltfile(t))["southfoundry:api"]
	if before == "" {
		t.Fatal("the seed sync wrote no southfoundry:api block")
	}

	setServicePort(t, other, "api", 8099)
	res, err := SyncScope(ScopeNames("navexa:backend"))
	if err != nil {
		t.Fatalf("SyncScope: %v", err)
	}

	if want := []string{"southfoundry:api"}; !reflect.DeepEqual(res.Kept, want) {
		t.Errorf("Kept = %v, want %v", res.Kept, want)
	}
	if slices.Contains(res.Changed, "southfoundry:api") {
		t.Errorf("Changed = %v, want it to omit the untargeted resource", res.Changed)
	}
	if got := resourceBlocks(hostTiltfile(t))["southfoundry:api"]; got != before {
		t.Errorf("the untargeted block was rewritten:\ngot:\n%s\nwant:\n%s", got, before)
	}
}

func TestSyncScopeWritesATargetedBlock(t *testing.T) {
	target, _ := seededWorkspaces(t)

	setServicePort(t, target, "backend", 8081)
	res, err := SyncScope(ScopeNames("navexa:backend"))
	if err != nil {
		t.Fatalf("SyncScope: %v", err)
	}

	if !res.Wrote {
		t.Error("Wrote = false, want the targeted change on disk")
	}
	if !slices.Contains(res.Changed, "navexa:backend") {
		t.Errorf("Changed = %v, want it to name navexa:backend", res.Changed)
	}
	if len(res.Kept) != 0 {
		t.Errorf("Kept = %v, want none", res.Kept)
	}
}

func TestSyncScopeStillAddsAndRemovesUntargetedResources(t *testing.T) {
	_, other := seededWorkspaces(t)

	makeRepo(t, filepath.Join(other.Path, "cache"), "cache", 8091)
	setSouthfoundryRepos(t, other, "api", "cache")
	if _, err := SyncScope(ScopeNames("navexa:backend")); err != nil {
		t.Fatalf("SyncScope after the add: %v", err)
	}
	if _, ok := resourceBlocks(hostTiltfile(t))["southfoundry:cache"]; !ok {
		t.Error("a new resource outside the scope was suppressed, want it added")
	}

	setSouthfoundryRepos(t, other, "api")
	if _, err := SyncScope(ScopeNames("navexa:backend")); err != nil {
		t.Fatalf("SyncScope after the removal: %v", err)
	}
	if _, ok := resourceBlocks(hostTiltfile(t))["southfoundry:cache"]; ok {
		t.Error("a removed resource outside the scope came back, want it gone")
	}
}

func TestScopeAllWritesEveryChangedBlock(t *testing.T) {
	_, other := seededWorkspaces(t)

	setServicePort(t, other, "api", 8099)
	res, err := SyncScope(ScopeAll())
	if err != nil {
		t.Fatalf("SyncScope: %v", err)
	}

	if len(res.Kept) != 0 {
		t.Errorf("Kept = %v, want none for ScopeAll", res.Kept)
	}
	if !slices.Contains(res.Changed, "southfoundry:api") {
		t.Errorf("Changed = %v, want it to name southfoundry:api", res.Changed)
	}
}

func TestRegenerateScopeAddsTheStackAndKeepsAnotherWorkspace(t *testing.T) {
	target, other := seededWorkspaces(t)
	before := resourceBlocks(hostTiltfile(t))["southfoundry:api"]
	if before == "" {
		t.Fatal("the seed sync wrote no southfoundry:api block")
	}

	base, err := workspace.FindByName(target.Name)
	if err != nil {
		t.Fatalf("find the base workspace: %v", err)
	}
	if _, err := stack.Create(stack.CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}}); err != nil {
		t.Fatalf("stack.Create: %v", err)
	}
	if err := stack.SetActive(base.Name, "feat", true); err != nil {
		t.Fatalf("stack.SetActive: %v", err)
	}
	setServicePort(t, other, "api", 8099)

	if _, _, err := RegenerateScope(ScopeStack(base.Name, "feat")); err != nil {
		t.Fatalf("RegenerateScope: %v", err)
	}

	blocks := resourceBlocks(hostTiltfile(t))
	if _, ok := blocks["navexa:backend:feat"]; !ok {
		t.Errorf("the stack's resource is missing; the file holds %v", sortedBlockNames(blocks))
	}
	if got := blocks["southfoundry:api"]; got != before {
		t.Errorf("the other workspace's block was rewritten:\ngot:\n%s\nwant:\n%s", got, before)
	}
}

func sortedBlockNames(blocks map[string]string) []string {
	names := make([]string, 0, len(blocks))
	for n := range blocks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func TestScopeStackCoversOnlyItsOwnNamespace(t *testing.T) {
	s := ScopeStack("navexa", "agent-harness")
	for _, name := range []string{"navexa:api:agent-harness", "navexa:worker:agent-harness"} {
		if !s.Covers(name) {
			t.Errorf("Covers(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"navexa:api", "navexa:api:other", "southfoundry:api:agent-harness"} {
		if s.Covers(name) {
			t.Errorf("Covers(%q) = true, want false", name)
		}
	}
}
