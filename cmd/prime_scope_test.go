package cmd

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
)

// A stack overlays a few services and borrows the rest from base. An agent sent
// to finish the feature reads the whole workspace as its subject unless the
// briefing draws the edge, and the services outside the overlay are base's
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
	writePrimeScope(&b, rec, "fx-rates", nil)
	got := b.String()

	for _, want := range []string{
		"SCOPE",
		"navexa-api, nxPriceService",
		"/home/nick/dev/.devstack-stacks/navexa/fx-rates/Navexa",
		"base is shared",
		"devstack stack add fx-rates",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the scope block must state %q, got:\n%s", want, got)
		}
	}
}

// The instruction is worthless if it does not say what to do when the feature
// genuinely needs another service. Without that, an agent that needs one either
// stops or edits base.
func TestBriefingSaysHowToWidenTheStack(t *testing.T) {
	rec := &stack.Record{Name: "perf", Overlay: []string{"api"}, Worktrees: map[string]string{"api": "/tmp/perf/api"}}

	var b strings.Builder
	writePrimeScope(&b, rec, "perf", nil)

	if !strings.Contains(b.String(), "devstack stack add perf <service>") {
		t.Errorf("the scope block must name the command that widens the stack, got:\n%s", b.String())
	}
}

func TestBriefingWritesNoScopeBlockForAStackWithNoOverlay(t *testing.T) {
	var b strings.Builder
	writePrimeScope(&b, &stack.Record{Name: "empty"}, "empty", nil)

	if b.String() != "" {
		t.Errorf("a stack with no overlay has no edge to draw, got:\n%s", b.String())
	}
}
