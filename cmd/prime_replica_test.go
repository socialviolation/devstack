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

// The briefing is what every session reads before it touches anything, so the
// two facts that decide whether it edits the right directory have to be in it:
// base runs from a replica rather than the checkout, and the checkout is a
// template. Without them an agent edits here and reports a fix that never ran.
func TestBriefingSaysBaseRunsFromTheReplica(t *testing.T) {
	var b strings.Builder
	writePrimeTerms(&b)
	terms := b.String()

	for _, want := range []string{"replica", "template", "devstack base sync"} {
		if !strings.Contains(terms, want) {
			t.Errorf("the briefing's terms must state %q, so an edit is not made in a directory nothing runs:\n%s", want, terms)
		}
	}
	if strings.Contains(terms, "base       your normal checkout") {
		t.Errorf("the briefing still defines base as the checkout:\n%s", terms)
	}
}

// The other half of the same rule: a command that changes what is running has
// no default instance. A briefing that says "if you do not give --stack, a
// command uses base" sends an agent to run one that is refused.
func TestBriefingSaysAMutatingCommandNeedsAnInstance(t *testing.T) {
	var b strings.Builder
	writePrimeTerms(&b)
	terms := b.String()

	if !strings.Contains(terms, "--stack base") {
		t.Errorf("the briefing must name --stack base as how base is targeted:\n%s", terms)
	}
	if !strings.Contains(terms, "no default") {
		t.Errorf("the briefing must say a mutating command has no default instance:\n%s", terms)
	}
	if strings.Contains(terms, "If you do not give --stack, a command uses base") {
		t.Errorf("the briefing still claims base is the implicit target:\n%s", terms)
	}
}

// Standing in the checkout, the briefing has to say so — and say where base
// really runs — rather than reporting the directory as base itself.
func TestBriefingNamesTheCheckoutATemplateAndPointsAtTheReplica(t *testing.T) {
	root := t.TempDir()
	ws := &workspace.Workspace{Name: "navexa", Path: filepath.Join(root, "navexa")}
	if err := os.MkdirAll(replica.Root(ws), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(replica.Root(ws), config.WorkspaceManifestFileName), "version: 1\nworkspace:\n  name: navexa\n")

	var b strings.Builder
	writePrimeIdentity(&b, ws, "api", "", "master", "base", false, nil)
	got := b.String()

	if !strings.Contains(got, "template checkout") {
		t.Errorf("identity = %q, want the checkout named as the template", got)
	}
	if !strings.Contains(got, ".devstack-base") {
		t.Errorf("identity = %q, want the replica path base runs from", got)
	}

	b.Reset()
	writePrimeIdentity(&b, ws, "api", "", "master", "base", true, nil)
	if got := b.String(); !strings.Contains(got, "base replica") || !strings.Contains(got, "do not edit here") {
		t.Errorf("identity in the replica = %q, want it named and marked read-only", got)
	}
}

// Until 'devstack workspace up' has built the replica, the daemon really does
// run the checkout. Saying otherwise here would be as wrong as the claim it
// replaced, so the briefing states the exception and how to end it.
func TestBriefingSaysSoWhileNoReplicaIsBuilt(t *testing.T) {
	ws := &workspace.Workspace{Name: "navexa", Path: filepath.Join(t.TempDir(), "navexa")}

	var b strings.Builder
	writePrimeIdentity(&b, ws, "api", "", "master", "base", false, nil)
	got := b.String()

	if !strings.Contains(got, "No replica is built yet") || !strings.Contains(got, "devstack workspace up") {
		t.Errorf("identity = %q, want the un-built replica named, with the command that builds it", got)
	}
	if strings.Contains(got, "Nothing runs here") {
		t.Errorf("identity = %q, must not deny that the checkout runs while it still does", got)
	}
}

// The reload verdict is the line an agent acts on after an edit. Every restart
// it prints has to name an instance, or the command it suggests is refused; and
// in the checkout it must not promise that an edit reloads into anything.
func TestReloadVerdictNamesAnInstanceAndDoesNotPromiseTheCheckoutReloads(t *testing.T) {
	rw := &config.ResolvedWorkspace{Services: map[string]config.ResolvedService{
		"api": {Manifest: &config.ServiceManifest{}},
	}}
	rw.Services["api"].Manifest.Runtime.Run.Command = "go run ."

	var b strings.Builder
	writePrimeReload(&b, rw, "api", "base", false, true, nil)
	got := b.String()

	if !strings.Contains(got, "devstack service restart api --stack base") {
		t.Errorf("reload verdict = %q, want a restart that names its instance", got)
	}
	if !strings.Contains(got, "base runs the replica") {
		t.Errorf("reload verdict = %q, want it to say an edit here does not reach base", got)
	}

	b.Reset()
	rw.Services["api"].Manifest.Runtime.Run.Command = "dotnet watch run"
	writePrimeReload(&b, rw, "api", "base", false, true, nil)
	if got := b.String(); strings.Contains(got, "Your code changes apply without a restart") {
		t.Errorf("reload verdict = %q, want no claim that an edit here reloads: base runs the replica", got)
	}
}

// The generated agent files are read without prime ever running, so the same
// two facts have to survive into them — and no mutating command may be shown
// with --stack as an optional extra.
func TestGeneratedAgentInstructionsRequireAnInstance(t *testing.T) {
	for name, block := range map[string]string{
		"AGENTS.md": buildAgentInstructions("api", t.TempDir(), "/home/dev/navexa", ""),
		"CLAUDE.md": buildAIInstructionPointer("api", ""),
	} {
		for _, want := range []string{"replica", "template", "--stack base"} {
			if !strings.Contains(block, want) {
				t.Errorf("%s never states %q:\n%s", name, want, block)
			}
		}
		for _, forbidden := range []string{
			"devstack service start api (add `--stack <name>`",
			"add `--stack <name>` for a stack's instance",
			"devstack service restart api [--stack <name>]",
			"devstack service stop api [--stack <name>]",
		} {
			if strings.Contains(block, forbidden) {
				t.Errorf("%s shows --stack as optional on a mutating command (%q):\n%s", name, forbidden, block)
			}
		}
	}
}

// A stack cut from the checkout's HEAD was the old behaviour, and an agent told
// so will commit onto a base it never had. The generated file states the rule.
func TestGeneratedAgentInstructionsStateWhereAStackIsCutFrom(t *testing.T) {
	block := buildAgentInstructions("api", t.TempDir(), "/home/dev/navexa", "")
	for _, want := range []string{"default branch", "--from"} {
		if !strings.Contains(block, want) {
			t.Errorf("AGENTS.md never says where a stack is cut from (%q):\n%s", want, block)
		}
	}
}

// The base row of the instance table has to print the worktree base RUNS. It
// printed the checkout, which is the one directory guaranteed not to be it.
func TestBaseRowNamesTheReplicaWorktreeNotTheCheckout(t *testing.T) {
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
		t.Errorf("base row directory = %q, want the replica worktree %q", dir, replicaAPI)
	}
}
