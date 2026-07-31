package cmd

import (
	"strings"
	"testing"
)

func TestFirstLineClipsAMultiLineNote(t *testing.T) {
	note := "NVXA-1422 wrong Holdings.Name\n\nLonger explanation nobody needs in a status header."
	got := firstLine(note, 58)
	if got != "NVXA-1422 wrong Holdings.Name" {
		t.Fatalf("firstLine() = %q, want just the headline", got)
	}
}

// A stack note is free prose and routinely a paragraph. Left whole it wraps the
// orientation block into noise, so the header takes the identifying line only.
func TestFirstLineTruncatesALongSingleLine(t *testing.T) {
	note := strings.Repeat("x", 200)
	got := firstLine(note, 58)
	if len([]rune(got)) != 58 {
		t.Fatalf("firstLine() length = %d, want 58", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated note should be marked with an ellipsis: %q", got)
	}
}

func TestFirstLineLeavesShortNotesAlone(t *testing.T) {
	if got := firstLine("NAV-388 agent control refactor", 58); got != "NAV-388 agent control refactor" {
		t.Fatalf("firstLine() = %q", got)
	}
}

func TestFirstLineTrimsSurroundingSpace(t *testing.T) {
	if got := firstLine("   spaced out   \nrest", 58); got != "spaced out" {
		t.Fatalf("firstLine() = %q, want %q", got, "spaced out")
	}
}

func TestContainsString(t *testing.T) {
	overlay := []string{"navexa-api", "navexa-frontend"}
	if !containsString(overlay, "navexa-api") {
		t.Error("containsString() missed a member")
	}
	if containsString(overlay, "nxOrbit") {
		t.Error("containsString() matched a non-member")
	}
	if containsString(nil, "anything") {
		t.Error("containsString(nil) should be false")
	}
}
