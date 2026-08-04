package migrate

import (
	"strings"
	"testing"
)

// The closing line is the one an agent acts on. A failure changes nothing, so it
// arrives here with no next action, and the empty-note branch used to answer it
// with "every migration is applied" — the verdict being wrong in exactly the
// case where acting on it costs the most.
func TestTheClosingLineNeverCallsAFailedRunApplied(t *testing.T) {
	var b strings.Builder
	writeNote(&b, nil, 1, 0)

	got := b.String()
	if strings.Contains(got, "Every migration is applied") {
		t.Errorf("a failed run must not report every migration applied, got:\n%s", got)
	}
	for _, want := range []string{"FAILED", "devstack migrate"} {
		if !strings.Contains(got, want) {
			t.Errorf("the closing line must state the failure and how to retry (%q missing), got:\n%s", want, got)
		}
	}
}

// A patch that changed something AND a patch that failed in the same run: the
// reader needs both the next action and the failure.
func TestAFailedRunStillCarriesTheNextActionOfWhatSucceeded(t *testing.T) {
	var b strings.Builder
	writeNote(&b, []string{"NOW COMMIT. these repositories hold uncommitted changes:"}, 1, 0)

	got := b.String()
	if !strings.Contains(got, "FAILED") || !strings.Contains(got, "NOW COMMIT") {
		t.Errorf("both the failure and the successful patch's next action must survive, got:\n%s", got)
	}
}

func TestACleanRunWithNothingToDoStillSaysSo(t *testing.T) {
	var b strings.Builder
	writeNote(&b, nil, 0, 0)

	if !strings.Contains(b.String(), "Every migration is applied") {
		t.Errorf("a clean no-op run must still report that, got:\n%s", b.String())
	}
}

// A patch that leaves a file only a person can change writes no new version, so
// the migration is still pending. "Every migration is applied" is the one
// verdict this run must not close with.
func TestTheClosingLineNeverCallsABlockedRunApplied(t *testing.T) {
	var b strings.Builder
	writeNote(&b, nil, 0, 1)

	got := b.String()
	if strings.Contains(got, "Every migration is applied") {
		t.Errorf("a run that left work for a person must not report every migration applied, got:\n%s", got)
	}
	for _, want := range []string{"NOT FINISHED", "stays pending", "devstack migrate"} {
		if !strings.Contains(got, want) {
			t.Errorf("the closing line must state the blocked work and how to finish it (%q missing), got:\n%s", want, got)
		}
	}
}
