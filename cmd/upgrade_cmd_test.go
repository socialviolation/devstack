package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/selfcheck"
	"github.com/socialviolation/devstack/internal/workspace"
)

// stubDevstack writes a fake devstack that records the arguments and working
// directory it was called with, so the migration can be driven without
// installing anything or writing into a real repo.
func stubDevstack(t *testing.T, log string, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devstack")
	script := "#!/bin/sh\necho \"$PWD $*\" >> " + log + "\nexit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Migration must run the newly installed binary, in each workspace's directory.
// Running this process instead would regenerate the files with the very content
// the upgrade was meant to replace.
func TestMigrationRunsTheInstalledBinaryInEachWorkspace(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	bin := stubDevstack(t, log, 0)

	err := migrateWorkspaces(bin, []workspace.Workspace{
		{Name: "alpha", Path: t.TempDir()},
		{Name: "beta", Path: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("migrateWorkspaces() = %v", err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one invocation per workspace, got %d: %q", len(lines), lines)
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, "init --all --claude-hook") {
			t.Errorf("migration must run the full refresh, got %q", l)
		}
	}
}

// The hook is what makes a session briefed at all, and a migration is the one
// place it is asked for — `init` alone leaves it opt-in, because
// .claude/settings.json is committed and adding one unasked is not a side
// effect a refresh should have.
func TestMigrationSetsUpTheSessionHook(t *testing.T) {
	var joined string
	for _, a := range migrateArgs {
		joined += a + " "
	}
	if !strings.Contains(joined, "--claude-hook") {
		t.Errorf("migrateArgs = %v, want the session hook wired up", migrateArgs)
	}
	if !strings.Contains(joined, "--all") {
		t.Errorf("migrateArgs = %v, want every service refreshed", migrateArgs)
	}
}

// One workspace failing must not strand the others, and the failure must be
// reported rather than swallowed.
func TestMigrationReportsFailureAndStillAttemptsEveryWorkspace(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	bin := stubDevstack(t, log, 1)

	err := migrateWorkspaces(bin, []workspace.Workspace{
		{Name: "alpha", Path: t.TempDir()},
		{Name: "beta", Path: t.TempDir()},
	})
	if err == nil {
		t.Fatal("a failed migration must be reported")
	}
	for _, name := range []string{"alpha", "beta"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error names no failure for %s: %v", name, err)
		}
	}
	data, _ := os.ReadFile(log)
	if n := len(strings.Split(strings.TrimSpace(string(data)), "\n")); n != 2 {
		t.Errorf("every workspace must still be attempted, got %d", n)
	}
}

// A build from an unpushed commit is the normal state while developing devstack
// itself, and it is newer than anything published. Installing @latest there
// replaces that work with the branch, from a command whose name promises the
// opposite — so it must refuse without --force.
func TestUpgradeRefusesToMoveALocalBuildBackwards(t *testing.T) {
	for _, status := range []selfcheck.Status{selfcheck.StatusLocal, selfcheck.StatusAhead} {
		if err := checkUpgradeWorthDoing(selfcheck.Result{Status: status, AheadBy: 2}, false); err == nil {
			t.Errorf("%s: upgrade must refuse rather than install an older published commit", status)
		}
		if err := checkUpgradeWorthDoing(selfcheck.Result{Status: status, AheadBy: 2}, true); err != nil {
			t.Errorf("%s with --force = %v, want it to proceed", status, err)
		}
	}
}

func TestUpgradeProceedsWhenBehind(t *testing.T) {
	if err := checkUpgradeWorthDoing(selfcheck.Result{Status: selfcheck.StatusBehind, BehindBy: 3}, false); err != nil {
		t.Errorf("a build that is behind is the case upgrade exists for: %v", err)
	}
}

// upgrade compares the stamp of the newly installed binary against the stamp in
// every generated file, so what it reads back out of `--version` has to be the
// whole stamp. Taking the second field dropped the commit — "v0.1.1" against a
// file written by "v0.1.1 (f1969ec)" — so nothing ever matched and upgrade
// reported every file in every workspace as stale, immediately after migrating
// them, while workspace doctor reported the same files as current.
func TestVersionOutputParsesBackToTheWholeStamp(t *testing.T) {
	for _, tc := range []struct{ out, want string }{
		{"devstack v0.1.1 (f1969ec)\n", "v0.1.1 (f1969ec)"},
		{"devstack dev (a615867, uncommitted changes)\n", "dev (a615867, uncommitted changes)"},
		{"devstack v0.1.1 (f1969ec)\n  12 commits behind main — update: go install x@latest\n", "v0.1.1 (f1969ec)"},
		{"devstack v0.1.1\n", "v0.1.1"},
	} {
		if got := parseVersionOutput(tc.out); got != tc.want {
			t.Errorf("parseVersionOutput(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

// The round trip is the actual contract: what --version prints must come back
// as exactly what is stamped into generated files, or the two disagree forever.
func TestStampSurvivesItsOwnVersionOutput(t *testing.T) {
	if got := parseVersionOutput("devstack " + buildStamp() + "\n"); got != buildStamp() {
		t.Errorf("stamp did not survive the round trip: printed %q, read back %q", buildStamp(), got)
	}
}

// upgrade and workspace doctor answer the same question and must answer it the
// same way. They compared different values, so one called a file stale while the
// other called it current — with no way to tell which was lying.
func TestUpgradeAndDoctorCompareTheSameValue(t *testing.T) {
	files := []generatedFile{{Service: "api", Exists: true, Version: buildStamp()}}

	if got := staleGenerated(files, buildStamp()); len(got) != 0 {
		t.Errorf("doctor calls a file written by this build stale: %+v", got)
	}
	// What upgrade passes when the install produced this same binary.
	upgradeTarget := parseVersionOutput("devstack " + buildStamp() + "\n")
	if got := staleGenerated(files, upgradeTarget); len(got) != 0 {
		t.Errorf("upgrade calls a file written by this build stale: %+v", got)
	}
}
