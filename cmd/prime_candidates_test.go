package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/stack"
)

func day(n int) time.Time { return time.Date(2026, 8, n, 9, 0, 0, 0, time.UTC) }

func entry(at time.Time, text string) []stack.NoteEntry {
	return []stack.NoteEntry{{At: at, Text: text}}
}

func TestARunningStackOutranksADownOne(t *testing.T) {
	got := rankStackCandidates([]stack.Record{
		{Name: "old-down", CreatedAt: day(4), Log: entry(day(5), "touched today")},
		{Name: "live", Active: true, CreatedAt: day(1)},
	})
	if got[0].Name != "live" {
		t.Fatalf("ranked %q first, want the stack that is up", got[0].Name)
	}
}

func TestTheNewestNoteEntryWinsWithinAState(t *testing.T) {
	got := rankStackCandidates([]stack.Record{
		{Name: "stale", Active: true, CreatedAt: day(5), Log: entry(day(1), "last week")},
		{Name: "fresh", Active: true, CreatedAt: day(1), Log: entry(day(5), "this morning")},
	})
	if got[0].Name != "fresh" {
		t.Fatalf("ranked %q first, want the stack with the newest note entry", got[0].Name)
	}
}

func TestAStackWithAnEntryOutranksOneWithout(t *testing.T) {
	got := rankStackCandidates([]stack.Record{
		{Name: "silent", CreatedAt: day(6)},
		{Name: "logged", CreatedAt: day(1), Log: entry(day(2), "got somewhere")},
	})
	if got[0].Name != "logged" {
		t.Fatalf("ranked %q first, want the stack that has a note entry", got[0].Name)
	}
}

func TestTheNewestStackBreaksARemainingTie(t *testing.T) {
	got := rankStackCandidates([]stack.Record{
		{Name: "older", CreatedAt: day(1)},
		{Name: "newer", CreatedAt: day(6)},
	})
	if got[0].Name != "newer" {
		t.Fatalf("ranked %q first, want the newest stack", got[0].Name)
	}
}

func TestTheCandidateRowsCarryTheNote(t *testing.T) {
	var b strings.Builder
	writePrimeCandidates(&b, []stack.Record{
		{Name: "fx-rates", Active: true, CreatedAt: day(1), Note: "NAV-412 daily value spike"},
		{Name: "orbit-split", CreatedAt: day(2), Note: "monorepo split"},
	})
	got := b.String()

	for _, want := range []string{"fx-rates", "up", "NAV-412 daily value spike", "orbit-split", "down", "monorepo split"} {
		if !strings.Contains(got, want) {
			t.Errorf("the candidate block never states %q:\n%s", want, got)
		}
	}
}

func TestACandidateRowIsMarkedAsAGuess(t *testing.T) {
	var b strings.Builder
	writePrimeCandidates(&b, []stack.Record{{Name: "fx-rates", Note: "NAV-412"}})
	got := b.String()

	if !strings.Contains(got, "? fx-rates") {
		t.Errorf("a candidate row must be marked ?:\n%s", got)
	}
	if strings.Contains(got, "▸") {
		t.Errorf("a candidate is a guess and must never be marked ▸:\n%s", got)
	}
	if !strings.Contains(got, "Ask the user before you work on one.") {
		t.Errorf("the candidate block must send the agent to the user:\n%s", got)
	}
}

func TestTheCandidateListIsBoundedAndSaysSo(t *testing.T) {
	var recs []stack.Record
	for i := 0; i < primeCandidateRows+3; i++ {
		recs = append(recs, stack.Record{Name: "stack-" + string(rune('a'+i)), CreatedAt: day(1 + i)})
	}
	var b strings.Builder
	writePrimeCandidates(&b, recs)
	got := b.String()

	if n := strings.Count(got, "  ? "); n != primeCandidateRows {
		t.Errorf("printed %d rows, want %d:\n%s", n, primeCandidateRows, got)
	}
	if !strings.Contains(got, "3 more. To see every one, run: devstack stack list") {
		t.Errorf("the block must say what it left out:\n%s", got)
	}
}

func TestNoStacksAddsNothingToTheTaskBlock(t *testing.T) {
	var b strings.Builder
	writePrimeCandidates(&b, nil)
	if got := b.String(); got != "" {
		t.Fatalf("an empty store must add nothing to the briefing, got:\n%s", got)
	}
}

func TestTheTaskBlockStillAsksWhenItHasCandidates(t *testing.T) {
	var b strings.Builder
	writePrimeTask(&b, "api", "base", nil, nil, false, false, []stack.Record{
		{Name: "fx-rates", Active: true, Note: "NAV-412 daily value spike"},
	})
	got := b.String()

	if !strings.Contains(got, "1. Ask the user which feature this session is for.") {
		t.Errorf("the task block must still ask:\n%s", got)
	}
	if !strings.Contains(got, "fx-rates") {
		t.Errorf("the task block must name the candidate it holds:\n%s", got)
	}
}
