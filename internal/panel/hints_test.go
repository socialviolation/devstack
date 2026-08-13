package panel

import (
	"regexp"
	"strings"
	"testing"
)

func hintModel(enterCopies bool) model {
	m := model{theme: LoadTheme(), enterCopies: enterCopies, width: 120, height: 24}
	m.style = newStyles(m.theme)
	return m
}

func TestThePickerNamesWhatEnterDoes(t *testing.T) {
	copying := hintModel(true)
	if copying.jumpTitle() != "copy" {
		t.Fatalf("picker title = %q, want copy", copying.jumpTitle())
	}
	if !strings.HasPrefix(copying.jumpHint(), "enter copies") {
		t.Fatalf("picker hint = %q, want it to say enter copies", copying.jumpHint())
	}
	// The action enter does not take still needs a key the reader can see.
	if !strings.Contains(copying.jumpHint(), "ctrl+o opens") {
		t.Fatalf("picker hint = %q, want the key that opens", copying.jumpHint())
	}

	opening := hintModel(false)
	if opening.jumpTitle() != "open" {
		t.Fatalf("picker title = %q, want open", opening.jumpTitle())
	}
	if !strings.HasPrefix(opening.jumpHint(), "enter opens") {
		t.Fatalf("picker hint = %q, want it to say enter opens", opening.jumpHint())
	}
	if !strings.Contains(opening.jumpHint(), "ctrl+y copies") {
		t.Fatalf("picker hint = %q, want the key that copies", opening.jumpHint())
	}
}

func TestTheBarNamesWhatEnterDoes(t *testing.T) {
	copying := stripANSI(hintModel(true).footer())
	if !strings.Contains(copying, "enter copy") {
		t.Fatalf("bar = %q, want it to say enter copy", copying)
	}
	if !strings.Contains(copying, "o open") {
		t.Fatalf("bar = %q, want the key that opens", copying)
	}

	opening := stripANSI(hintModel(false).footer())
	if !strings.Contains(opening, "enter open") {
		t.Fatalf("bar = %q, want it to say enter open", opening)
	}
	if !strings.Contains(opening, "y copy") {
		t.Fatalf("bar = %q, want the key that copies", opening)
	}
}

// stripANSI drops the colour so a test reads the words the reader reads.
func stripANSI(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(s, "")
}
