package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// A stack's purpose says why it exists; its latest entry says where it got to.
// An agent briefed into a session it did not start needs the second one to avoid
// redoing work that is already done, so the briefing carries both.
func TestBriefingCarriesTheLatestEntryOfTheInferredStack(t *testing.T) {
	rec := &stack.Record{
		Name: "perf",
		Base: "navexa",
		Note: "NAV-412 daily value spike",
		Log: []stack.NoteEntry{
			{At: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC), Text: "repro on dev2"},
			{At: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), Text: "parked: needs a prod diff"},
		},
		Worktrees: map[string]string{"navexa-api": "/home/nick/dev/.devstack-stacks/perf/navexa-api"},
	}

	var b strings.Builder
	writePrimeIdentity(&b, &workspace.Workspace{Name: "navexa"}, "navexa-api", "", "master", "base",
		&workingStack{Rec: rec, Reason: "branch match"})

	got := b.String()
	if !strings.Contains(got, "purpose NAV-412 daily value spike") {
		t.Fatalf("briefing = %q, want the purpose", got)
	}
	if !strings.Contains(got, "latest  2026-08-01  parked: needs a prod diff") {
		t.Fatalf("briefing = %q, want the newest entry, dated", got)
	}
	if strings.Contains(got, "repro on dev2") {
		t.Fatalf("briefing = %q, want only the newest entry, not the whole log", got)
	}
}

func TestBriefingOmitsTheEntryLineWhenAStackHasNoLog(t *testing.T) {
	rec := &stack.Record{Name: "perf", Base: "navexa", Note: "NAV-412 daily value spike"}

	var b strings.Builder
	writePrimeIdentity(&b, &workspace.Workspace{Name: "navexa"}, "navexa-api", "", "master", "base",
		&workingStack{Rec: rec, Reason: "branch match"})

	if got := b.String(); strings.Contains(got, "latest") {
		t.Fatalf("briefing = %q, want no latest line without entries", got)
	}
}
