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

// The migration must run the newly installed binary. Running this process
// instead would leave behind exactly what the new one removes. One command
// sweeps the whole machine, so one call is right and a call per workspace is
// not.
func TestMigrationRunsTheInstalledBinaryOnce(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	bin := stubDevstack(t, log, 0)

	if err := runMigration(bin); err != nil {
		t.Fatalf("runMigration() = %v", err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one invocation for the machine, got %d: %q", len(lines), lines)
	}
	if !strings.HasSuffix(lines[0], "migrate") {
		t.Errorf("the upgrade must run `devstack migrate`, got %q", lines[0])
	}
}

// The hook is what makes a session briefed at all, and a migration is the one
// place it is asked for — `init` alone leaves it opt-in, because
// .claude/settings.json is committed and adding one unasked is not a side
// effect a refresh should have.
func TestMigrationWiresTheSessionHookWithEveryMatcher(t *testing.T) {
	dir := t.TempDir()
	lines, res := migrateOne(migrateTarget{Label: "api", Dir: dir, Service: "api"})

	if res.Hooks != 1 {
		t.Fatalf("migrateOne() wired %d hooks, want 1: %q", res.Hooks, lines)
	}
	got := primeMatchers(t, readSettings(t, filepath.Join(dir, claudeSettingsRel)))
	if strings.Join(got, ",") != strings.Join(primeHookMatchers, ",") {
		t.Fatalf("briefed matchers = %v, want %v", got, primeHookMatchers)
	}
	if res.MCP != 1 {
		t.Errorf("migrateOne() wrote no .mcp.json: %q", lines)
	}
}

// A failed migration must be reported, and not swallowed.
func TestMigrationReportsItsFailure(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	bin := stubDevstack(t, log, 1)

	err := runMigration(bin)
	if err == nil {
		t.Fatal("a failed migration must be reported")
	}
	if !strings.Contains(err.Error(), "devstack migrate") {
		t.Errorf("the error does not name the command to run: %v", err)
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

// The round trip is the contract with `upgrade`: what --version prints must come
// back as exactly what the binary stamps itself with.
func TestStampSurvivesItsOwnVersionOutput(t *testing.T) {
	if got := parseVersionOutput("devstack " + buildStamp() + "\n"); got != buildStamp() {
		t.Errorf("stamp did not survive the round trip: printed %q, read back %q", buildStamp(), got)
	}
}

// upgrade and workspace doctor answer the same question and must answer it the
// same way. They read different values once, so one called a file stale while
// the other called it current, with no way to tell which was lying. Both now
// count the same scan.
func TestUpgradeAndDoctorCountTheSameFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, agentsFileName),
		"# api\n\nMine.\n\n"+agentsSentinelBegin+"\ngenerated\n"+agentsSentinelEnd+"\n")

	found := scanResidue(dir)
	if len(found) != 1 {
		t.Fatalf("scanResidue() = %+v, want the one file that holds a devstack block", found)
	}
	if n := len(stripDir(dir)); n != len(found) {
		t.Errorf("the report counts %d files and the migration changes %d", len(found), n)
	}
}

// The report is a report. Reading it must change no file, or a user who ran
// `upgrade` to see the damage has already taken it.
func TestTheResidueReportChangesNothing(t *testing.T) {
	dir := t.TempDir()
	seed := "# api\n\nMine.\n\n" + agentsSentinelBegin + "\ngenerated\n" + agentsSentinelEnd + "\n"
	writeFile(t, filepath.Join(dir, agentsFileName), seed)

	var b strings.Builder
	writeResidueReport(&b, map[string][]residueFile{"navexa": scanResidue(dir)},
		[]workspace.Workspace{{Name: "navexa"}}, false)

	if got := readString(t, filepath.Join(dir, agentsFileName)); got != seed {
		t.Fatalf("the report changed the file:\n%s", got)
	}
	if !strings.Contains(b.String(), "devstack upgrade --migrate") {
		t.Errorf("the report never names the command that cleans this up:\n%s", b.String())
	}
}
