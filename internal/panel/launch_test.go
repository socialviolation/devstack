package panel

import (
	"strings"
	"testing"
)

const panesJSON = `{"result": {"panes": [
  {"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "focused": true},
  {"pane_id": "w1:p2", "tab_id": "w1:t1", "workspace_id": "w1", "label": "devstack"},
  {"pane_id": "w2:p1", "tab_id": "w2:t1", "workspace_id": "w2"}
]}}`

const inTab1 = `{"workspace_id": "w1", "tab_id": "w1:t1", "focused_pane_id": "w1:p1", "focused_pane_cwd": "/home/nick/dev/navexa"}`

func decide(t *testing.T, panes, context string, inTab bool) string {
	t.Helper()
	return LaunchDecision(strings.NewReader(panes), context, inTab)
}

// One key opens the panel and takes it away again. A launcher that only ever
// opens leaves a tab full of panels.
func TestThePanelOfThisTabIsFocused(t *testing.T) {
	if got := decide(t, panesJSON, inTab1, false); got != "FOCUS w1:p2" {
		t.Errorf("decision = %q, want FOCUS w1:p2", got)
	}
}

func TestThePanelClosesWhenItIsTheFocusedPane(t *testing.T) {
	context := `{"workspace_id": "w1", "tab_id": "w1:t1", "focused_pane_id": "w1:p2"}`
	if got := decide(t, panesJSON, context, false); got != "CLOSE w1:p2" {
		t.Errorf("decision = %q, want CLOSE w1:p2", got)
	}
}

// The panel shows the workspace it opens in, so it has to open in the directory
// the reader works in, and not in the plugin's own directory.
func TestAFreshPanelOpensInTheDirectoryOfTheReader(t *testing.T) {
	panes := `{"result": {"panes": [{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "focused": true}]}}`
	if got := decide(t, panes, inTab1, false); got != "OPEN /home/nick/dev/navexa" {
		t.Errorf("decision = %q, want OPEN with the working directory", got)
	}
}

// A panel in another tab is out of reach of the split placement, which works
// inside one tab. Opening a second one there is right.
func TestTheSplitPlacementIgnoresAPanelInAnotherTab(t *testing.T) {
	panes := `{"result": {"panes": [
      {"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "focused": true},
      {"pane_id": "w1:p9", "tab_id": "w1:t2", "workspace_id": "w1", "label": "devstack"}]}}`

	if got := decide(t, panes, inTab1, false); got != "OPEN /home/nick/dev/navexa" {
		t.Errorf("decision = %q, want OPEN", got)
	}
	if got := decide(t, panes, inTab1, true); got != "SWITCHTAB w1:t2" {
		t.Errorf("tab decision = %q, want SWITCHTAB w1:t2", got)
	}
}

// A panel in another workspace belongs to somebody else's work. The action
// opens one here rather than moving the reader across the session.
func TestAPanelInAnotherWorkspaceIsLeftAlone(t *testing.T) {
	panes := `{"result": {"panes": [
      {"pane_id": "w2:p9", "tab_id": "w2:t1", "workspace_id": "w2", "label": "devstack"}]}}`

	if got := decide(t, panes, inTab1, true); got != "OPEN /home/nick/dev/navexa" {
		t.Errorf("decision = %q, want OPEN", got)
	}
}

// herdr answers over a socket, and the launcher must still open a panel when
// that answer is missing or unreadable.
func TestABrokenPaneListStillOpensThePanel(t *testing.T) {
	for _, panes := range []string{"", "not json", `{"error": {"code": "no"}}`} {
		if got := decide(t, panes, inTab1, false); got != "OPEN /home/nick/dev/navexa" {
			t.Errorf("decision for %q = %q, want OPEN", panes, got)
		}
	}
}

// Without a herdr context there is no directory to name, and OPEN with no
// directory is what the launcher falls back on.
func TestAMissingContextStillOpensThePanel(t *testing.T) {
	if got := decide(t, panesJSON, "", false); got != "OPEN " {
		t.Errorf("decision = %q, want a bare OPEN", got)
	}
}

// herdr runs a plugin action from the plugin's own directory, which belongs to
// no workspace. A panel opened there shows every workspace on the machine
// instead of the one the reader pressed the key in.
func TestThePanelOpensInTheDirectoryOfTheFocusedPane(t *testing.T) {
	panes := `{"result": {"panes": [
      {"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "focused": true,
       "cwd": "/home/nick/dev/navexa", "foreground_cwd": "/home/nick/dev/navexa/nxOrbit"}]}}`
	context := `{"workspace_id": "w1", "tab_id": "w1:t1", "focused_pane_id": "w1:p1"}`

	if got := decide(t, panes, context, false); got != "OPEN /home/nick/dev/navexa/nxOrbit" {
		t.Errorf("decision = %q, want the directory the pane sits in", got)
	}
	if got := LaunchCwd(strings.NewReader(panes), context); got != "/home/nick/dev/navexa/nxOrbit" {
		t.Errorf("LaunchCwd = %q, want the directory the pane sits in", got)
	}
}

// The context that herdr passes is the first word. The pane list is what
// answers when that context carries no directory.
func TestTheContextDirectoryBeatsThePaneList(t *testing.T) {
	panes := `{"result": {"panes": [
      {"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "focused": true,
       "foreground_cwd": "/home/nick/dev/other"}]}}`

	if got := LaunchCwd(strings.NewReader(panes), inTab1); got != "/home/nick/dev/navexa" {
		t.Errorf("LaunchCwd = %q, want the context's directory", got)
	}
}
