package cmd

import (
	"strings"
	"testing"
)

// The doctor is where a reader meets this residue, and it used to name only
// `devstack migrate` — the command that changes files in repositories devstack
// does not own. The read-only preview has to come first, so a reader can see
// what goes before anything goes.
func TestDoctorPointsAtThePreviewBeforeTheMigration(t *testing.T) {
	ws, _ := migrateToolWorkspace(t)

	var b strings.Builder
	if n := reportDevstackResidue(&b, ws.Path); n != 2 {
		t.Fatalf("reportDevstackResidue() = %d files, want 2:\n%s", n, b.String())
	}
	got := b.String()

	preview := strings.Index(got, "devstack migrate --list")
	if preview < 0 {
		t.Fatalf("the doctor never names the read-only preview:\n%s", got)
	}
	run := strings.Index(got, "Then remove it: devstack migrate")
	if run < 0 {
		t.Fatalf("the doctor never names the command that removes the residue:\n%s", got)
	}
	if preview > run {
		t.Errorf("the doctor names the migration before the preview:\n%s", got)
	}
}
