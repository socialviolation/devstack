package cmd

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
)

// stateWords is every word devstack may print as the state of something. A stack
// is up or down; a copy has one of the rest. Adding a word here is a decision to
// make an agent learn one more, so the list is meant to stay short.
var stateWords = map[string]bool{
	stack.StatusUp:   true,
	stack.StatusDown: true,
	"running":        true,
	"starting":       true,
	"building":       true,
	"erroring":       true,
	"stopped":        true,
	"disabled":       true,
	"unknown":        true,
}

// One stack was called "active" by stack list, "idle" by status and "stopped" by
// the briefing, and nothing said the three described one thing. Every state a
// surface prints must be a word from the list, and every word on the list must
// be defined in the briefing — an agent that meets a state it cannot look up has
// to guess, and the guesses were what sent reviewers to the wrong conclusion.
func TestStateWordsAreDefinedOnce(t *testing.T) {
	var b strings.Builder
	writePrimeTerms(&b)
	terms := b.String()

	// Each copy state has to head its own definition line. A bare substring
	// match would pass on "the process is up and healthy", which defines
	// nothing.
	for _, word := range []string{"running", "starting", "building", "erroring", "stopped", "disabled", stack.StatusDown} {
		if !strings.Contains(terms, "\n  "+word+" ") {
			t.Errorf("the briefing prints %q as a state but never defines it — add it to writePrimeStates", word)
		}
	}
	if !strings.Contains(terms, "A stack is "+stack.StatusUp+" or "+stack.StatusDown) {
		t.Errorf("the briefing must say a stack's two states are %q and %q", stack.StatusUp, stack.StatusDown)
	}
}

// serviceStatus is the one function that names a copy's state, so its whole
// range has to be inside the vocabulary. It returned "active" for a copy whose
// stack was up but whose resource was missing, a word no legend ever defined.
func TestServiceStatusReturnsOnlyVocabulary(t *testing.T) {
	cases := []tilt.UIResource{
		{Status: tilt.UIResourceStatus{RuntimeStatus: "ok"}},
		{Status: tilt.UIResourceStatus{RuntimeStatus: "pending"}},
		{Status: tilt.UIResourceStatus{RuntimeStatus: "error"}},
		{Status: tilt.UIResourceStatus{UpdateStatus: "running"}},
		{Status: tilt.UIResourceStatus{UpdateStatus: "error"}},
		{Status: tilt.UIResourceStatus{}},
		{Status: tilt.UIResourceStatus{DisableStatus: &tilt.DisableStatus{State: "Disabled"}}},
	}
	for _, r := range cases {
		if got := serviceStatus(r); !stateWords[got] {
			t.Errorf("serviceStatus() = %q, which is not a state word any legend defines", got)
		}
	}
}

// stackStatus is the only source of a stack's state. A caller in another package
// compared it against the literal "active" and silently took the wrong branch
// when the word changed, so the two constants are the contract.
func TestStackStateIsUpOrDown(t *testing.T) {
	if stack.StatusUp == stack.StatusDown {
		t.Fatal("a stack's two states must differ")
	}
	for _, s := range []string{stack.StatusUp, stack.StatusDown} {
		if !stateWords[s] {
			t.Errorf("stack state %q is outside the vocabulary", s)
		}
	}
}

// "idle" was the condensed-section marker in status and appeared on no other
// surface, so a reader could not map it onto any state in the legend. The marker
// describes the section, not a state, and must not read as one.
func TestCondensedMarkerIsNotAStateWord(t *testing.T) {
	marker := strings.TrimSpace(condensedMarker)
	if stateWords[marker] {
		t.Errorf("the condensed marker %q reads as a state a service is in; it describes the section", marker)
	}
	if strings.Contains(marker, "idle") {
		t.Error(`"idle" appears on no other devstack surface, so it cannot be looked up`)
	}
}
