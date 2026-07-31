package cmd

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Comparing a string's length in bytes and then slicing it as runes panics on
// any string that is longer in bytes than in runes, which is every non-ASCII
// string. A stack note or a branch name with an accent in it crashed whichever
// command printed it — prime and status both do.
func TestTruncationHelpersSurviveMultibyteInput(t *testing.T) {
	accented := strings.Repeat("é", 30) // 60 bytes, 30 runes
	mixed := "Café — refonte du système de paiement pour les clients européens"
	emoji := strings.Repeat("🙂", 40)

	for _, in := range []string{accented, mixed, emoji} {
		for _, n := range []int{0, 1, 2, 5, 28, 46, 58, 84} {
			if got := clipRunes(in, n); utf8.ValidString(got) == false {
				t.Errorf("clipRunes(%d) produced invalid UTF-8", n)
			}
			if got := firstLine(in, n); !utf8.ValidString(got) {
				t.Errorf("firstLine(%d) produced invalid UTF-8", n)
			}
			if got := truncateCell(in, n); !utf8.ValidString(got) {
				t.Errorf("truncateCell(%d) produced invalid UTF-8", n)
			}
			if got := fitCell(in, n); !utf8.ValidString(got) {
				t.Errorf("fitCell(%d) produced invalid UTF-8", n)
			}
		}
	}
}

// Clipping counts printed characters, not bytes, or a column of accented names
// comes out visibly shorter than its ASCII neighbours.
func TestClipRunesCountsCharactersNotBytes(t *testing.T) {
	if got := clipRunes("ééééé", 10); got != "ééééé" {
		t.Errorf("clipRunes = %q, want the whole string: 5 runes fit in 10", got)
	}
	got := clipRunes(strings.Repeat("é", 20), 10)
	if n := len([]rune(got)); n != 10 {
		t.Errorf("clipRunes produced %d runes, want 10", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a clipped string should be marked: %q", got)
	}
}

// fitCell keeps the trailing "*" that marks uncommitted work, because that is
// the part of a branch label that changes what you do next.
func TestFitCellKeepsTheDirtyMarker(t *testing.T) {
	got := fitCell("nick/a-very-long-feature-branch-name-indeed*", 20)
	if !strings.HasSuffix(got, "..*") {
		t.Fatalf("fitCell = %q, want the dirty marker kept", got)
	}
}
