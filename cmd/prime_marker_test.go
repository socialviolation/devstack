package cmd

import (
	"strings"
	"testing"
)

// The ▸ marker is a claim about the filesystem: this is the directory the
// command ran in. It was previously put on the INFERRED stack, so standing in
// base with one candidate stack produced a briefing whose header said "base"
// and whose table said you were in the stack — with that stack's worktree path
// printed beside it, which made the wrong claim look verified. An agent acting
// on it edits a worktree nobody asked about.
func TestMarkersSeparateWhereYouAreFromWhatIsInferred(t *testing.T) {
	rows := []string{"base", "nvxa-1422"}
	here := "base"
	suggested := "nvxa-1422"

	var marks []string
	for _, name := range rows {
		marker := " "
		switch name {
		case here:
			marker = "▸"
		case suggested:
			marker = "?"
		}
		marks = append(marks, marker+name)
	}

	got := strings.Join(marks, ",")
	if got != "▸base,?nvxa-1422" {
		t.Fatalf("markers = %q, want the checkout marked ▸ and the guess marked ?", got)
	}
}

// When the inferred stack IS the directory you are in, there is no guess to
// mark — a second marker would imply two candidates where there is one fact.
func TestNoGuessMarkerWhenTheInferenceMatchesTheCheckout(t *testing.T) {
	here := "nvxa-1422"
	working := "nvxa-1422"

	suggested := ""
	if working != here {
		suggested = working
	}
	if suggested != "" {
		t.Fatalf("suggested = %q, want none when the inference is where you already are", suggested)
	}
}
