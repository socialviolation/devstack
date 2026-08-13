package cmd

import (
	"strings"
	"testing"
)

func TestPanelEnterSetting(t *testing.T) {
	// A browser started here reaches this reader, so the default opens.
	t.Setenv("SSH_TTY", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")

	for _, tc := range []struct {
		setting string
		copies  bool
	}{
		{"", false},
		{"auto", false},
		{"open", false},
		{"copy", true},
		{" COPY ", true},
	} {
		copies, err := panelEnterCopies(tc.setting)
		if err != nil {
			t.Fatalf("panelEnterCopies(%q): %v", tc.setting, err)
		}
		if copies != tc.copies {
			t.Fatalf("panelEnterCopies(%q) = %v, want %v", tc.setting, copies, tc.copies)
		}
	}
}

func TestPanelEnterRejectsAnythingElseAndSaysHow(t *testing.T) {
	_, err := panelEnterCopies("paste")
	if err == nil {
		t.Fatal("expected an error for an unknown setting")
	}
	if !strings.Contains(err.Error(), "--enter copy") {
		t.Fatalf("the error does not say how to set it: %v", err)
	}
}

func TestPanelEnterCopiesInsideHerdr(t *testing.T) {
	t.Setenv("SSH_TTY", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	t.Setenv("HERDR_ENV", "1")

	// ssh -> shell -> herdr -> shell: the pane sees the server's environment,
	// so a display here says nothing about where the reader sits.
	copies, err := panelEnterCopies("")
	if err != nil {
		t.Fatalf("panelEnterCopies(): %v", err)
	}
	if !copies {
		t.Fatal("the default opened a browser in a herdr pane, where the reader can be elsewhere")
	}

	// A reader who runs herdr on the machine they sit at says so, and wins.
	copies, err = panelEnterCopies("open")
	if err != nil || copies {
		t.Fatalf("panelEnterCopies(open) = %v, %v; the explicit choice must win", copies, err)
	}
}
