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

// A stack that is up has a process running now, which outranks every stack that
// has none.
func TestARunningStackOutranksADownOne(t *testing.T) {
	got := rankStackCandidates([]stack.Record{
		{Name: "old-down", CreatedAt: day(4), Log: entry(day(5), "touched today")},
		{Name: "live", Active: true, CreatedAt: day(1)},
	})
	if got[0].Name != "live" {
		t.Fatalf("ranked %q first, want the stack that is up", got[0].Name)
	}
}

// Between two stacks in the same state, the one somebody wrote a note entry on
// most recently is the better guess.
func TestTheNewestNoteEntryWinsWithinAState(t *testing.T) {
	got := rankStackCandidates([]stack.Record{
		{Name: "stale", Active: true, CreatedAt: day(5), Log: entry(day(1), "last week")},
		{Name: "fresh", Active: true, CreatedAt: day(1), Log: entry(day(5), "this morning")},
	})
	if got[0].Name != "fresh" {
		t.Fatalf("ranked %q first, want the stack with the newest note entry", got[0].Name)
	}
}

// A stack with any entry beats one with none, whatever the dates say, because
// an absent entry is no evidence rather than old evidence.
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

// The whole point of the list: the agent asks a closed question. So the note
// that identifies each stack has to be on the row.
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

// A candidate is a guess about intent. The briefing marks a guess ? and reserves
// ▸ for the directory the caller is actually in, so a candidate row must never
// carry ▸.
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

// The briefing is generated into every session against a character budget, so
// the list is bounded and says what it left out.
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

// With no stacks in the store there is nothing to rank, and the task block must
// read exactly as it did before.
func TestNoStacksAddsNothingToTheTaskBlock(t *testing.T) {
	var b strings.Builder
	writePrimeCandidates(&b, nil)
	if got := b.String(); got != "" {
		t.Fatalf("an empty store must add nothing to the briefing, got:\n%s", got)
	}
}

// The list gives the question its material. It must not replace the question.
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
