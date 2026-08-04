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

// primeInstancesWorkspace builds a workspace whose replica is built, so the
// checkout it returns is the template that base does not run.
func primeInstancesWorkspace(t *testing.T) (*workspace.Workspace, *config.ResolvedWorkspace) {
	t.Helper()
	root := t.TempDir()
	ws := &workspace.Workspace{Name: "navexa", Path: filepath.Join(root, "navexa")}
	replicaAPI := filepath.Join(replica.Root(ws), "api")
	if err := os.MkdirAll(replicaAPI, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(replica.Root(ws), config.WorkspaceManifestFileName),
		"version: 1\nworkspace:\n  name: navexa\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - "+replicaAPI+"\n")
	writeFile(t, filepath.Join(replicaAPI, config.ServiceManifestFileName),
		"version: 1\nservice:\n  name: api\nruntime:\n  run:\n    command: go run .\n")

	rw := &config.ResolvedWorkspace{Services: map[string]config.ResolvedService{
		"api": {RepoPath: filepath.Join(ws.Path, "api"), Manifest: &config.ServiceManifest{}},
	}}
	return ws, rw
}

// The daemon runs, and it holds no copy of this service. The STATES block of
// this same briefing calls that "down", `devstack status --help` calls it
// "down", and the MCP status tool calls it "down". The base row printed
// "(none)", which no surface defines, so the reader met a state word that the
// page beside it never explained. Every stack row already printed "down".
func TestTheBaseRowPrintsDownWhenTheDaemonHoldsNoCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, rw := primeInstancesWorkspace(t)
	serveDaemon(t, `{"uiResources":[]}`)

	var b strings.Builder
	writePrimeInstances(&b, ws, rw, "api", "base", nil, false)
	got := b.String()

	if strings.Contains(got, "(none)") {
		t.Errorf("the base row prints a state word that no surface defines:\n%s", got)
	}
	if !strings.Contains(got, "base") || !strings.Contains(got, "down") {
		t.Errorf("the base row does not print `down`:\n%s", got)
	}
	var b2 strings.Builder
	writePrimeStates(&b2)
	if !strings.Contains(b2.String(), "  down  ") {
		t.Errorf("the STATES block does not define `down`, so the base row states an undefined word:\n%s", b2.String())
	}
}

// The template checkout is no copy. This same briefing says "Nothing runs here"
// and names the replica that base runs, and then it marked the base row ▸ and
// printed the replica's path beside it. ▸ is the one marker that claims a
// filesystem fact, so a false one reads as verified.
func TestNoMarkerOnTheBaseRowInATemplateCheckout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, rw := primeInstancesWorkspace(t)
	serveDaemon(t, `{"uiResources":[]}`)

	if !inTemplateCheckout(ws, "base", false) {
		t.Fatal("the replica is built and the caller is not in it, so this is a template checkout")
	}

	var b strings.Builder
	writePrimeInstances(&b, ws, rw, "api", "base", nil, true)
	got := b.String()

	if strings.Contains(got, "▸ base") {
		t.Errorf("the table marks a copy the caller is not in:\n%s", got)
	}
	if strings.Contains(got, "you are in now") {
		t.Errorf("the table claims the caller is in a copy on it:\n%s", got)
	}
	if !strings.Contains(got, "template checkout, which is no copy") {
		t.Errorf("the table never says why no row is marked:\n%s", got)
	}
}

// In the replica, and in a stack worktree, ▸ is true: the caller stands in the
// directory that copy runs. The fix must not take the marker away there.
func TestTheMarkerStaysWhereTheCallerIsReallyInACopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, rw := primeInstancesWorkspace(t)
	serveDaemon(t, `{"uiResources":[]}`)

	if inTemplateCheckout(ws, "base", true) {
		t.Error("the replica is not the template checkout")
	}

	var b strings.Builder
	writePrimeInstances(&b, ws, rw, "api", "base", nil, false)
	got := b.String()

	if !strings.Contains(got, "▸ base") {
		t.Errorf("the base row lost its marker where the marker is true:\n%s", got)
	}
	if !strings.Contains(got, "The marker ▸ shows the copy that you are in now: base.") {
		t.Errorf("the table never explains the marker it prints:\n%s", got)
	}
}
