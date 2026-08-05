package cmd

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
)

// A stack overlays a few services and borrows the rest from base. An agent sent
// to finish the feature reads the whole workspace as its subject unless the
// task block draws the edge, and the services outside the overlay are base's
// copies — shared with the user and with every other stack.
func TestBriefingNamesTheServicesTheStackOwns(t *testing.T) {
	rec := &stack.Record{
		Name:    "fx-rates",
		Base:    "navexa",
		Overlay: []string{"navexa-api", "nxPriceService"},
		Worktrees: map[string]string{
			"navexa-api":     "/home/nick/dev/.devstack-stacks/navexa/fx-rates/Navexa",
			"nxPriceService": "/home/nick/dev/.devstack-stacks/navexa/fx-rates/nxPriceService",
		},
	}

	var b strings.Builder
	writePrimeStackTask(&b, rec, "navexa-api", nil, false)
	got := b.String()

	for _, want := range []string{
		"stack fx-rates · navexa-api, nxPriceService",
		"1. Change code in these directories, and in no others:",
		"/home/nick/dev/.devstack-stacks/navexa/fx-rates/Navexa",
		"every other stack share base",
		"devstack stack add fx-rates",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the task block must state %q, got:\n%s", want, got)
		}
	}
}

// The task block is worthless if it is not a loop: an edit that is never
// restarted runs nowhere, and work that is never recorded is done twice. Each
// step names the stack, so the agent runs it rather than deriving it.
func TestTheTaskBlockIsTheLoopWithRealNames(t *testing.T) {
	rec := &stack.Record{
		Name:      "fx-rates",
		Branch:    "nick/fx",
		Overlay:   []string{"navexa-api", "nxPriceService"},
		Worktrees: map[string]string{"navexa-api": "/tmp/fx-rates/navexa-api"},
	}

	var b strings.Builder
	writePrimeStackTask(&b, rec, "navexa-api", nil, true)
	got := b.String()

	for _, want := range []string{
		"devstack service restart navexa-api --stack fx-rates",
		"devstack otel traces --stack fx-rates",
		`devstack stack note fx-rates --add "what you found"`,
		"git branch -d nick/fx",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the task block must name %q with the real names, got:\n%s", want, got)
		}
	}
}

// The trace tool is registered only where the workspace has observability, so a
// task block that always names it sends half the workspaces to a tool that is
// not there.
func TestTheTaskBlockNamesTheTraceToolOnlyWhereThereIsTelemetry(t *testing.T) {
	rec := &stack.Record{Name: "perf", Overlay: []string{"api"}, Worktrees: map[string]string{"api": "/tmp/perf/api"}}

	var b strings.Builder
	writePrimeStackTask(&b, rec, "api", nil, false)

	if got := b.String(); strings.Contains(got, "otel traces") || strings.Contains(got, "investigate") {
		t.Errorf("this workspace has no observability, so neither surface exists:\n%s", got)
	}
}

// The restart step has to name a service even in a directory that names none —
// the sibling directory of a shared repository is one.
func TestTheTaskBlockRestartsAServiceTheStackRuns(t *testing.T) {
	rec := &stack.Record{Name: "feat", Overlay: []string{"web"}, Worktrees: map[string]string{"web": "/tmp/feat/web"}}

	var b strings.Builder
	writePrimeStackTask(&b, rec, "", nil, false)

	if !strings.Contains(b.String(), "devstack service restart web --stack feat") {
		t.Errorf("the restart step must name a service the stack runs, got:\n%s", b.String())
	}
}

// The instruction is worthless if it does not say what to do when the feature
// genuinely needs another service. Without that, an agent that needs one either
// stops or edits base.
func TestBriefingSaysHowToWidenTheStack(t *testing.T) {
	rec := &stack.Record{Name: "perf", Overlay: []string{"api"}, Worktrees: map[string]string{"api": "/tmp/perf/api"}}

	var b strings.Builder
	writePrimeStackTask(&b, rec, "api", nil, false)

	if !strings.Contains(b.String(), "devstack stack add perf <service>") {
		t.Errorf("the task block must name the command that widens the stack, got:\n%s", b.String())
	}
}

// A stack the session is not standing in is a guess about intent. The task
// block has to ask before it edits anything, and it has to name the directory
// the work belongs in — an agent that acts on a guess edits a worktree nobody
// asked about.
func TestTheTaskBlockAsksBeforeItActsOnAGuessedStack(t *testing.T) {
	rec := &stack.Record{
		Name:      "bybit",
		Branch:    "nick/bybit",
		Overlay:   []string{"nxFileProcessor"},
		Worktrees: map[string]string{"nxFileProcessor": "/tmp/bybit/nxFileProcessor"},
	}

	var b strings.Builder
	writePrimeTask(&b, "nxFileProcessor", "base", &workingStack{Rec: rec, Reason: "this is the only stack that runs nxFileProcessor"}, nil, false, false, nil)
	got := b.String()

	for _, want := range []string{
		"a guess: this is the only stack that runs nxFileProcessor",
		"1. Ask the user: is this session for bybit?",
		"Change no code until you have the answer",
		"/tmp/bybit/nxFileProcessor",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the task block never states %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "1. Change code") {
		t.Errorf("the stack is a guess, so no step may open with an edit:\n%s", got)
	}
}

// With no stack in sight the task block asks, and it says how a change is made
// to run at all. Naming a stack here would be a guess with no evidence.
func TestTheTaskBlockAsksWhenThereIsNoStack(t *testing.T) {
	var b strings.Builder
	writePrimeTask(&b, "api", "base", nil, nil, false, false, nil)
	got := b.String()

	for _, want := range []string{
		"no stack",
		"1. Ask the user which feature this session is for.",
		"devstack stack create <name> --repos <service>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the task block never states %q:\n%s", want, got)
		}
	}
}

// In the replica, "your change reaches base on the default branch" is the wrong
// warning: this directory IS what base runs, and `workspace up` overwrites it.
func TestTheTaskBlockSaysTheReplicaIsOverwritten(t *testing.T) {
	var b strings.Builder
	writePrimeTask(&b, "api", "base", nil, nil, false, true, nil)

	if got := b.String(); !strings.Contains(got, "overwrites it. Do not edit here.") {
		t.Errorf("the task block must say an edit in the replica is overwritten:\n%s", got)
	}
}

func TestBriefingListsNoDirectoriesForAStackWithNoOverlay(t *testing.T) {
	var b strings.Builder
	writePrimeStackTask(&b, &stack.Record{Name: "empty"}, "", nil, false)
	got := b.String()

	if strings.Contains(got, "Change code in these directories") {
		t.Errorf("a stack with no overlay has no directory to name, got:\n%s", got)
	}
	if !strings.Contains(got, "devstack stack add empty <service>") {
		t.Errorf("a stack with no overlay must be told how to get one, got:\n%s", got)
	}
}
