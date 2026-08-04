package cmd

import (
	"strings"
	"testing"
)

// An upgrade of devstack is also an upgrade of the configuration in
// repositories devstack does not own, and of the code the running copies serve.
// A reader who learns that from the report has already had it done to them.
func TestUpgradeStatesWhatItChangesBeforeItChangesIt(t *testing.T) {
	var b strings.Builder
	writeUpgradeIntent(&b, false, false)

	got := b.String()
	for _, want := range []string{
		"migrates your configuration",
		"writes files in your repositories",
		"builds the replica",
		"restarts each copy that runs",
		"--no-migrate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the intent must state %q, got:\n%s", want, got)
		}
	}
}

// The opt-out must describe the machine the caller ends up with, not repeat the
// default. --no-migrate leaves the configuration on its current version, and a
// reader has to know that is what they chose.
func TestUpgradeIntentDescribesWhatEachOptOutLeaves(t *testing.T) {
	var noMigrate strings.Builder
	writeUpgradeIntent(&noMigrate, true, false)
	if got := noMigrate.String(); !strings.Contains(got, "stays on its current version") || !strings.Contains(got, "migrate --list") {
		t.Errorf("--no-migrate must say the configuration is unchanged and how to read what is pending, got:\n%s", got)
	}
	if got := noMigrate.String(); strings.Contains(got, "restarts each copy") {
		t.Errorf("--no-migrate restarts nothing, so it must not promise a restart, got:\n%s", got)
	}

	var noRestart strings.Builder
	writeUpgradeIntent(&noRestart, false, true)
	if got := noRestart.String(); !strings.Contains(got, "keeps serving the old code") {
		t.Errorf("--no-restart must say the running copies are left on the old code, got:\n%s", got)
	}
}
