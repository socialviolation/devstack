package panel

import (
	"reflect"
	"testing"
)

// A terminal in application-cursor mode sends the arrow keys as SS3, not CSI.
// Read as three keys, Up quits the panel and the stray O opens the picker.
func TestTheArrowKeysArriveInTwoShapes(t *testing.T) {
	for _, data := range []string{"\x1b[A", "\x1bOA"} {
		if got := parseKeys([]byte(data)); !reflect.DeepEqual(got, []string{"up"}) {
			t.Errorf("parseKeys(%q) = %v, want [up]", data, got)
		}
	}
	if got := parseKeys([]byte("\x1bOB\x1bOC\x1bOD")); !reflect.DeepEqual(got, []string{"down", "right", "left"}) {
		t.Errorf("parseKeys of three SS3 arrows = %v", got)
	}
}

// A slow link splits an escape sequence over two reads. Half a sequence must
// not read as a key of its own: "escape" closed the whole panel.
func TestHalfOfAnEscapeSequenceIsNoKey(t *testing.T) {
	for _, data := range []string{"\x1b[", "\x1bO", "\x1b[1;"} {
		if got := parseKeys([]byte(data)); len(got) != 0 {
			t.Errorf("parseKeys(%q) = %v, want no key at all", data, got)
		}
	}
}

func TestAnEscapeOnItsOwnIsTheEscapeKey(t *testing.T) {
	if got := parseKeys([]byte{0x1b}); !reflect.DeepEqual(got, []string{"escape"}) {
		t.Errorf("parseKeys(ESC) = %v, want [escape]", got)
	}
}

// The panel quits on q and on ctrl+c. It must not quit on escape, which a
// terminal also sends as the first byte of every arrow key.
func TestEscapeDoesNotQuitTheList(t *testing.T) {
	m := &model{style: newStyles(terminalTheme())}
	if quit := m.handleKey("escape", make(chan commandResult, 1)); quit {
		t.Error("escape quit the panel")
	}
	if quit := m.handleKey("q", make(chan commandResult, 1)); !quit {
		t.Error("q did not quit the panel")
	}
}

// The picker rebuilds its list on every reading of the machine, and a service
// that finishes starting reorders it. A cursor held by number then opens
// whatever moved under it.
func TestThePickerHoldsTheAddressUnderTheCursor(t *testing.T) {
	m := &model{jump: true, style: newStyles(terminalTheme())}
	m.snap = Snapshot{Workspaces: []Workspace{{Name: "w", Base: []Service{
		{Name: "api", State: "stopped", URLs: []string{"https://box:8401"}},
		{Name: "web", State: "running", URLs: []string{"https://box:8402"}},
	}}}}
	m.refilterLinks()

	if m.links[0].URL != "https://box:8402" {
		t.Fatalf("first link = %q, want the running copy", m.links[0].URL)
	}
	m.linkIndex = 1
	picked := m.links[1].URL

	m.snap.Workspaces[0].Base[0].State = "running"
	m.refilterLinks()

	if m.links[m.linkIndex].URL != picked {
		t.Errorf("cursor now on %q, want %q: the list reordered under it",
			m.links[m.linkIndex].URL, picked)
	}
}
