package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

func TestBriefingTermsDefineTheSharedSetAndTheReplica(t *testing.T) {
	var b strings.Builder
	writePrimeTerms(&b)
	terms := b.String()

	for _, want := range []string{"shared set", "replica", "default branch", "needs", "calls", "stack_extend", "devstack stack up <name>"} {
		if !strings.Contains(terms, want) {
			t.Errorf("the briefing's terms must state %q:\n%s", want, terms)
		}
	}
	if regexp.MustCompile(`\bbase\b`).MatchString(terms) {
		t.Errorf("the briefing's terms still name base:\n%s", terms)
	}
}

func TestBriefingSaysAMutatingCommandNamesItsStack(t *testing.T) {
	var b strings.Builder
	writePrimeTerms(&b)
	terms := b.String()

	for _, want := range []string{
		"names the stack with `--stack <name>`",
		"Outside a stack, it acts only on a shared service",
		"It can not start, stop or restart one",
		"names the copy it changed",
	} {
		if !strings.Contains(terms, want) {
			t.Errorf("the briefing never states %q:\n%s", want, terms)
		}
	}
}

func TestBriefingNamesTheCheckoutAndPointsAtTheReplica(t *testing.T) {
	root := t.TempDir()
	ws := &workspace.Workspace{Name: "navexa", Path: filepath.Join(root, "navexa")}
	if err := os.MkdirAll(replica.Root(ws), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(replica.Root(ws), config.WorkspaceManifestFileName), "version: 1\nworkspace:\n  name: navexa\n")

	var b strings.Builder
	writePrimeIdentity(&b, ws, "api", "", false, true)
	got := b.String()

	if !strings.Contains(got, "checkout (not a stack)") || !strings.Contains(got, "Nothing runs here") {
		t.Errorf("identity = %q, want the checkout named, and that nothing runs it", got)
	}
	if !strings.Contains(got, replica.Root(ws)) {
		t.Errorf("identity = %q, want the replica path the shared set runs from", got)
	}

	b.Reset()
	writePrimeIdentity(&b, ws, "api", "", true, true)
	if got := b.String(); !strings.Contains(got, "replica of the shared set") || !strings.Contains(got, "do not edit here") {
		t.Errorf("identity in the replica = %q, want it named and marked read-only", got)
	}
}

func TestBriefingSaysSoWhileNoReplicaIsBuilt(t *testing.T) {
	ws := &workspace.Workspace{Name: "navexa", Path: filepath.Join(t.TempDir(), "navexa")}

	var b strings.Builder
	writePrimeIdentity(&b, ws, "api", "", false, true)
	got := b.String()

	if !strings.Contains(got, "devstack has built no replica yet") || !strings.Contains(got, "devstack workspace up") {
		t.Errorf("identity = %q, want the un-built replica named, with the command that builds it", got)
	}
	if strings.Contains(got, "Nothing runs here") {
		t.Errorf("identity = %q, must not deny that the checkout runs while it still does", got)
	}
}

func TestBriefingSaysNothingRunsTheCheckoutOfAWorkspaceWithNoSharedSet(t *testing.T) {
	ws := &workspace.Workspace{Name: "navexa", Path: filepath.Join(t.TempDir(), "navexa")}

	var b strings.Builder
	writePrimeIdentity(&b, ws, "api", "", false, false)
	got := b.String()

	if !strings.Contains(got, "Nothing runs here") || strings.Contains(got, "replica") {
		t.Errorf("identity = %q, want nothing runs here, and no replica named", got)
	}
}

func TestReloadVerdictNamesAStackAndDoesNotPromiseTheCheckoutReloads(t *testing.T) {
	rw := &config.ResolvedWorkspace{Services: map[string]config.ResolvedService{
		"api": {Manifest: &config.ServiceManifest{}},
	}}
	rw.Services["api"].Manifest.Runtime.Run.Command = "go run ."

	var b strings.Builder
	writePrimeReload(&b, rw, "api", "", false, true, nil)
	got := b.String()

	if !strings.Contains(got, "devstack service restart api --stack <name>") {
		t.Errorf("reload verdict = %q, want a restart that names its stack", got)
	}
	if !strings.Contains(got, "no copy runs your checkout") {
		t.Errorf("reload verdict = %q, want it to say an edit here reaches no copy", got)
	}

	b.Reset()
	rw.Manifest = &config.WorkspaceManifest{Shared: []string{"api"}}
	writePrimeReload(&b, rw, "api", "", false, true, nil)
	if got := b.String(); !strings.Contains(got, "devstack service restart api\n") {
		t.Errorf("reload verdict = %q, want a shared service restarted with no --stack", got)
	}
}

func TestStackCreateHelpStatesWhereAStackIsCutFrom(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"stack", "create"})
	if err != nil {
		t.Fatalf("stack create is not a registered command: %v", err)
	}
	help := cmd.Long + "\n" + cmd.Short
	if f := cmd.Flags().Lookup("from"); f != nil {
		help += "\n" + f.Usage
	} else {
		t.Error("stack create takes no --from, so a caller can not cut from another ref")
	}
	if !strings.Contains(help, "default branch") {
		t.Errorf("stack create never says it cuts from the default branch:\n%s", help)
	}
}

func TestSharedRowNamesTheReplicaWorktreeNotTheCheckout(t *testing.T) {
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

	dir, built := replicaDir(ws, "api", filepath.Join(ws.Path, "api"))
	if !built {
		t.Fatalf("replicaDir reported no replica while one is built at %s", replica.Root(ws))
	}
	if dir != replicaAPI {
		t.Errorf("shared row directory = %q, want the replica worktree %q", dir, replicaAPI)
	}
}
