package panel

import (
	"strings"
	"testing"
)

func linkModel(enterCopies bool, out *strings.Builder) *model {
	m := &model{
		theme:       LoadTheme(),
		enterCopies: enterCopies,
		out:         out,
		jump:        true,
		links:       []link{{URL: "https://example.test:8443"}},
	}
	m.style = newStyles(m.theme)
	return m
}

func TestEnterCopiesInThePickerWhenTheReaderChoseCopy(t *testing.T) {
	t.Setenv("SSH_TTY", "/dev/pts/3")
	var out strings.Builder
	m := linkModel(true, &out)

	m.handleJumpKey("enter")

	if !strings.HasPrefix(out.String(), "\x1b]52;c;") {
		t.Fatalf("enter wrote %q, want the address copied", out.String())
	}
	if !strings.HasPrefix(m.status, "copied ") {
		t.Fatalf("status = %q, want it to report a copy", m.status)
	}
}

func TestCtrlOStillOpensWhenEnterCopies(t *testing.T) {
	var out strings.Builder
	m := linkModel(true, &out)

	m.handleJumpKey("ctrl+o")

	if out.String() != "" {
		t.Fatalf("ctrl+o copied instead of opening: wrote %q", out.String())
	}
}

func TestThePickerPrintsWhatItTookAfterItCloses(t *testing.T) {
	t.Setenv("SSH_TTY", "/dev/pts/3")
	var out strings.Builder
	m := linkModel(true, &out)
	m.jumpOnly = true

	if quit := m.handleJumpKey("enter"); !quit {
		t.Fatal("the picker stayed open after it took an address")
	}
	// The status bar dies with the alternate screen, so the panel has to say
	// what it took on the terminal underneath.
	if !strings.Contains(m.parting, "copied https://example.test:8443") {
		t.Fatalf("parting = %q, want the copied address", m.parting)
	}
}

func TestHelpNamesWhatEnterDoes(t *testing.T) {
	if !strings.Contains(strings.Join(helpLines(true), "\n"), "enter        copy the address") {
		t.Fatal("the keys overlay does not say that enter copies")
	}
	if !strings.Contains(strings.Join(helpLines(false), "\n"), "enter        open the address") {
		t.Fatal("the keys overlay does not say that enter opens")
	}
}
