package stack

import (
	"strings"
	"testing"
)

func seedStack(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := saveStore("navexa", []Record{{Name: "perf", Base: "navexa", Note: "NAV-412 daily value spike"}}); err != nil {
		t.Fatalf("saveStore: %v", err)
	}
}

func logOf(t *testing.T, name string) []NoteEntry {
	t.Helper()
	rec, err := FindStack("navexa", name)
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	return rec.Log
}

func TestAppendNoteKeepsPurposeAndAddsEntry(t *testing.T) {
	seedStack(t)

	appended, entry, err := AppendNote("navexa", "perf", "cache warms on boot, spike is in the FX join")
	if err != nil {
		t.Fatalf("AppendNote: %v", err)
	}
	if !appended {
		t.Fatal("appended = false, want true for a first entry")
	}
	if entry.At.IsZero() {
		t.Fatal("entry has no timestamp")
	}

	rec, err := FindStack("navexa", "perf")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if rec.Note != "NAV-412 daily value spike" {
		t.Fatalf("Note = %q, want the purpose left untouched", rec.Note)
	}
	if len(rec.Log) != 1 || rec.Log[0].Text != "cache warms on boot, spike is in the FX join" {
		t.Fatalf("Log = %v, want the one appended entry", rec.Log)
	}
	if latest, ok := rec.LatestEntry(); !ok || latest.Text != rec.Log[0].Text {
		t.Fatalf("LatestEntry = %v, %v, want the appended entry", latest, ok)
	}
}

func TestAppendNoteCollapsesWhitespaceToOneLine(t *testing.T) {
	seedStack(t)

	if _, _, err := AppendNote("navexa", "perf", "  blocked on\n\tccxt rate limit  "); err != nil {
		t.Fatalf("AppendNote: %v", err)
	}
	got := logOf(t, "perf")[0].Text
	if got != "blocked on ccxt rate limit" {
		t.Fatalf("Text = %q, want whitespace collapsed to one line", got)
	}
}

func TestAppendNoteRejectsEmpty(t *testing.T) {
	seedStack(t)

	appended, _, err := AppendNote("navexa", "perf", "   \n  ")
	if err == nil {
		t.Fatal("AppendNote of blank text returned no error")
	}
	if appended {
		t.Fatal("appended = true for blank text")
	}
	if len(logOf(t, "perf")) != 0 {
		t.Fatal("blank text was written to the log")
	}
}

func TestAppendNoteRejectsOverlongEntry(t *testing.T) {
	seedStack(t)

	long := strings.Repeat("x", NoteEntryMax+1)
	appended, _, err := AppendNote("navexa", "perf", long)
	if err == nil {
		t.Fatalf("AppendNote of %d characters returned no error", len(long))
	}
	if appended {
		t.Fatal("appended = true for an overlong entry")
	}
	if len(logOf(t, "perf")) != 0 {
		t.Fatal("an overlong entry was written to the log")
	}
}

func TestAppendNoteRepeatingTheLastEntryIsANoop(t *testing.T) {
	seedStack(t)

	if _, _, err := AppendNote("navexa", "perf", "still chasing the FX join"); err != nil {
		t.Fatalf("first AppendNote: %v", err)
	}
	appended, _, err := AppendNote("navexa", "perf", "Still chasing the FX join")
	if err != nil {
		t.Fatalf("second AppendNote: %v", err)
	}
	if appended {
		t.Fatal("appended = true for a repeat of the last entry")
	}
	if got := logOf(t, "perf"); len(got) != 1 {
		t.Fatalf("Log has %d entries, want the repeat dropped", len(got))
	}
}

func TestAppendNoteKeepsOnlyTheLastEntries(t *testing.T) {
	seedStack(t)

	for _, text := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		if _, _, err := AppendNote("navexa", "perf", text); err != nil {
			t.Fatalf("AppendNote(%q): %v", text, err)
		}
	}

	got := logOf(t, "perf")
	if len(got) != NoteLogEntries {
		t.Fatalf("Log has %d entries, want the cap of %d", len(got), NoteLogEntries)
	}
	if got[0].Text != "three" || got[len(got)-1].Text != "seven" {
		t.Fatalf("Log = %v, want the oldest dropped and the newest kept", got)
	}
}

func TestSetNoteReplacesPurposeAndKeepsLog(t *testing.T) {
	seedStack(t)

	if _, _, err := AppendNote("navexa", "perf", "spike reproduced on dev2"); err != nil {
		t.Fatalf("AppendNote: %v", err)
	}
	if err := SetNote("navexa", "perf", "NAV-412 daily value spike (now with a repro)"); err != nil {
		t.Fatalf("SetNote: %v", err)
	}

	rec, err := FindStack("navexa", "perf")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if rec.Note != "NAV-412 daily value spike (now with a repro)" {
		t.Fatalf("Note = %q, want the new purpose", rec.Note)
	}
	if len(rec.Log) != 1 {
		t.Fatalf("Log = %v, want the entries kept when the purpose is rewritten", rec.Log)
	}
}

func TestSetNoteEmptyClearsLogToo(t *testing.T) {
	seedStack(t)

	if _, _, err := AppendNote("navexa", "perf", "spike reproduced on dev2"); err != nil {
		t.Fatalf("AppendNote: %v", err)
	}
	if err := SetNote("navexa", "perf", ""); err != nil {
		t.Fatalf("SetNote: %v", err)
	}

	rec, err := FindStack("navexa", "perf")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if rec.Note != "" || len(rec.Log) != 0 {
		t.Fatalf("Note = %q, Log = %v, want both cleared", rec.Note, rec.Log)
	}
}

func TestAppendNoteUnknownStack(t *testing.T) {
	seedStack(t)

	if _, _, err := AppendNote("navexa", "nope", "anything"); err == nil {
		t.Fatal("AppendNote on an unknown stack returned no error")
	}
}
