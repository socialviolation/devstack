package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

func primeInstancesWorkspace(t *testing.T) (*workspace.Workspace, *config.ResolvedWorkspace) {
	t.Helper()
	root := t.TempDir()
	ws := &workspace.Workspace{Name: "navexa", Path: filepath.Join(root, "navexa")}
	replicaAPI := filepath.Join(replica.Root(ws), "api")
	if err := os.MkdirAll(replicaAPI, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(replica.Root(ws), config.WorkspaceManifestFileName),
		"version: 1\nworkspace:\n  name: navexa\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - "+replicaAPI+"\nshared:\n  - api\n")
	writeFile(t, filepath.Join(replicaAPI, config.ServiceManifestFileName),
		"version: 1\nservice:\n  name: api\nruntime:\n  run:\n    command: go run .\n")

	rw := &config.ResolvedWorkspace{
		Manifest: &config.WorkspaceManifest{Shared: []string{"api"}},
		Services: map[string]config.ResolvedService{
			"api": {RepoPath: filepath.Join(ws.Path, "api"), Manifest: &config.ServiceManifest{}},
		},
	}
	return ws, rw
}

func TestTheSharedRowPrintsDownWhenTheDaemonHoldsNoCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, rw := primeInstancesWorkspace(t)
	serveDaemon(t, `{"uiResources":[]}`)

	var b strings.Builder
	writePrimeInstances(&b, ws, rw, "api", "", nil, false)
	got := b.String()

	if strings.Contains(got, "(none)") {
		t.Errorf("the shared row prints a state word that no surface defines:\n%s", got)
	}
	if !strings.Contains(got, "shared") || !strings.Contains(got, "down") {
		t.Errorf("the shared row does not print `down`:\n%s", got)
	}
	if strings.Contains(got, " base ") {
		t.Errorf("the table still names base:\n%s", got)
	}
	var b2 strings.Builder
	writePrimeStates(&b2)
	if !strings.Contains(b2.String(), "  down  ") {
		t.Errorf("the STATES block does not define `down`, so the shared row states an undefined word:\n%s", b2.String())
	}
}

func TestNoMarkerOnTheSharedRowInTheCheckout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, rw := primeInstancesWorkspace(t)
	serveDaemon(t, `{"uiResources":[]}`)

	if !inTemplateCheckout(ws, "", false) {
		t.Fatal("the replica is built and the caller is not in it, so this is the checkout")
	}

	var b strings.Builder
	writePrimeInstances(&b, ws, rw, "api", "", nil, true)
	got := b.String()

	if strings.Contains(got, "▸ shared") {
		t.Errorf("the table marks a copy the caller is not in:\n%s", got)
	}
	if strings.Contains(got, "you are in now") {
		t.Errorf("the table claims the caller is in a copy on it:\n%s", got)
	}
	if !strings.Contains(got, "your checkout, which runs no copy") {
		t.Errorf("the table never says why no row is marked:\n%s", got)
	}
}

func TestTheMarkerStaysWhereTheCallerIsReallyInACopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, rw := primeInstancesWorkspace(t)
	serveDaemon(t, `{"uiResources":[]}`)

	if inTemplateCheckout(ws, "", true) {
		t.Error("the replica is not the checkout")
	}

	var b strings.Builder
	writePrimeInstances(&b, ws, rw, "api", "", nil, false)
	got := b.String()

	if !strings.Contains(got, "▸ shared") {
		t.Errorf("the shared row lost its marker where the marker is true:\n%s", got)
	}
	if !strings.Contains(got, "The marker ▸ shows the copy that you are in now: shared.") {
		t.Errorf("the table never explains the marker it prints:\n%s", got)
	}
}

func TestAStackServiceHasNoSharedRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, rw := primeInstancesWorkspace(t)
	rw.Manifest.Shared = nil
	serveDaemon(t, `{"uiResources":[]}`)

	var b strings.Builder
	writePrimeInstances(&b, ws, rw, "api", "", nil, false)
	got := b.String()

	if strings.Contains(got, "shared ") && strings.Contains(got, "runs as") {
		t.Errorf("a stack service runs only in a stack, so the table must have no shared row:\n%s", got)
	}
	if !strings.Contains(got, "No stack runs api") || !strings.Contains(got, "devstack stack create <name> --repos api") {
		t.Errorf("the briefing must say no stack runs the service, and how to cut one:\n%s", got)
	}
}
