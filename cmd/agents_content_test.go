package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const humanAbove = "# api\n\nThis service owns billing. Read `docs/ledger.md` before you change a rate.\n\n" +
	"## House rules\n\n- Run `make lint` before every commit.\n- Never widen a decimal column.\n"

const humanBelow = "## BEADS\n\n- bead workflow notes a human wrote\n- and a second line, indented oddly\n     like this\n"

// humanBetween sits between two generated blocks. A remover that cuts from the
// first marker to the last one eats it, and nothing else would notice.
const humanBetween = "## Deploy notes\n\nThe staging deploy needs the migration first.\n"

// block renders what an older devstack committed between its markers.
func block(body string) string {
	return agentsSentinelBegin + "\n" + body + "\n" + agentsSentinelEnd
}

// The rule that matters most: devstack removes only what devstack wrote. A
// human's prose above and below the block comes out byte for byte the way it
// went in.
func TestRemovalLeavesHumanProseByteIdentical(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, agentsFileName)
	writeFile(t, agents, humanAbove+"\n"+
		block("stale generated content")+"\n\n"+
		humanBetween+"\n"+
		block("a duplicate an older devstack appended")+"\n\n"+
		legacyAgentsHeader+"\n\nunsentinelled content from before the markers existed\n\n"+
		humanBelow)

	changes := stripDir(dir)
	if len(changes) != 1 || changes[0].Action != actionRemoved {
		t.Fatalf("stripDir() = %+v, want one removal", changes)
	}

	got := readString(t, agents)
	if !strings.Contains(got, humanAbove) {
		t.Errorf("the prose above the block was altered:\n%s", got)
	}
	if !strings.Contains(got, humanBelow) {
		t.Errorf("the prose below the block was altered:\n%s", got)
	}
	if !strings.Contains(got, humanBetween) {
		t.Errorf("the prose between the two blocks was eaten:\n%s", got)
	}
	for _, gone := range []string{
		agentsSentinelBegin, agentsSentinelEnd, legacyAgentsHeader,
		"stale generated content", "a duplicate an older devstack",
		"unsentinelled content from before",
	} {
		if strings.Contains(got, gone) {
			t.Errorf("devstack content survived (%q):\n%s", gone, got)
		}
	}
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Errorf("want exactly one trailing newline, got %q", got[len(got)-3:])
	}
}

// A legacy section ends where the next section begins. Everything after it is
// somebody else's, and it must survive.
func TestRemovalOfALegacySectionKeepsTheSectionAfterIt(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, agentsFileName)
	writeFile(t, agents, "# api\n\nSome preamble.\n\n"+
		legacyAgentsHeader+"\n\nstale legacy content referencing devstack workspace doctor\n\n"+
		humanBelow)

	if changes := stripDir(dir); len(changes) != 1 || changes[0].Action != actionRemoved {
		t.Fatalf("stripDir() = %+v, want one removal", changes)
	}

	got := readString(t, agents)
	if strings.Contains(got, legacyAgentsHeader) || strings.Contains(got, "stale legacy content") {
		t.Errorf("the legacy section survived:\n%s", got)
	}
	if !strings.Contains(got, "Some preamble.") || !strings.Contains(got, humanBelow) {
		t.Errorf("content that is not devstack's was lost:\n%s", got)
	}
}

// A file that holds devstack content only is a file devstack created the need
// for. With the content gone there is nothing left to commit.
func TestAFileOfDevstackContentOnlyIsDeleted(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, agentsFileName)
	writeFile(t, agents, block("everything here was generated")+"\n")

	changes := stripDir(dir)
	if len(changes) != 1 || changes[0].Action != actionDeleted {
		t.Fatalf("stripDir() = %+v, want the file deleted", changes)
	}
	if _, err := os.Stat(agents); !os.IsNotExist(err) {
		t.Errorf("%s still exists (stat err = %v)", agents, err)
	}
}

// The pointer files belong to other tools. devstack takes its own block out of
// them and nothing else.
func TestPointerFilesLoseOnlyTheDevstackBlock(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, "CLAUDE.md")
	other := "## Some other tool\n\nIts own instructions, which devstack must not touch.\n"
	writeFile(t, claude, "# House rules\n\nAlways run the linter.\n\n"+
		block("stale pointer")+"\n\n"+
		block("duplicate pointer")+"\n\n"+
		legacyPointerHeader+"\n\nan old unsentinelled pointer block\n\n"+
		other)

	if changes := stripDir(dir); len(changes) != 1 || changes[0].Rel != "CLAUDE.md" {
		t.Fatalf("stripDir() = %+v, want CLAUDE.md changed", changes)
	}

	got := readString(t, claude)
	if !strings.Contains(got, "Always run the linter.") || !strings.Contains(got, other) {
		t.Errorf("content that is not devstack's was altered:\n%s", got)
	}
	for _, gone := range []string{"stale pointer", "duplicate pointer", "an old unsentinelled pointer block", legacyPointerHeader} {
		if strings.Contains(got, gone) {
			t.Errorf("devstack content survived (%q):\n%s", gone, got)
		}
	}
}

// A file devstack never wrote into is a file devstack never opens for writing.
func TestAFileDevstackNeverTouchedIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, agentsFileName)
	mine := "# api\n\nEverything here is mine.\n\n## devstack is mentioned, and that is all\n\nNo block, no markers.\n"
	writeFile(t, agents, mine)

	if changes := stripDir(dir); len(changes) != 0 {
		t.Fatalf("stripDir() = %+v, want nothing reported", changes)
	}
	if got := readString(t, agents); got != mine {
		t.Fatalf("the file changed:\n--- want ---\n%s\n--- got ---\n%s", mine, got)
	}
}

// devstack never created these files, so a removal never creates one either.
func TestRemovalCreatesNoFile(t *testing.T) {
	dir := t.TempDir()
	if changes := stripDir(dir); len(changes) != 0 {
		t.Fatalf("stripDir() on an empty directory = %+v, want nothing", changes)
	}
	for _, f := range contentFiles(dir) {
		if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
			t.Errorf("%s was created (stat err = %v)", f.Rel, err)
		}
	}
}

// A second run must change nothing, or every migration shows up as a diff.
func TestRemovalIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, agentsFileName)
	writeFile(t, agents, humanAbove+"\n"+block("stale")+"\n\n"+block("duplicate")+"\n\n"+humanBelow)

	if changes := stripDir(dir); len(changes) != 1 {
		t.Fatalf("first run = %+v, want one change", changes)
	}
	first := readString(t, agents)

	if changes := stripDir(dir); len(changes) != 0 {
		t.Fatalf("second run = %+v, want nothing to do", changes)
	}
	if second := readString(t, agents); first != second {
		t.Fatalf("not byte-identical on the second run:\n--- first ---\n%q\n--- second ---\n%q", first, second)
	}
}

// The guarantee that makes this safe to run unattended: where devstack can not
// find the end of its own block, it changes nothing at all. A remover that
// guesses eats the text a human wrote after the truncated block.
func TestAnUnpairedMarkerLeavesTheFileForAHuman(t *testing.T) {
	for name, seed := range map[string]string{
		"begin with no end": humanAbove + "\n" + agentsSentinelBegin + "\ntruncated block\n\n" + humanBelow,
		"end with no begin": humanAbove + "\n" + "orphan tail\n" + agentsSentinelEnd + "\n\n" + humanBelow,
	} {
		dir := t.TempDir()
		agents := filepath.Join(dir, agentsFileName)
		writeFile(t, agents, seed)

		changes := stripDir(dir)
		if len(changes) != 1 {
			t.Fatalf("%s: stripDir() = %+v, want one report", name, changes)
		}
		if changes[0].Changed() {
			t.Errorf("%s: devstack changed a file whose block has no end: %+v", name, changes[0])
		}
		if !strings.Contains(changes[0].Reason, "hand") {
			t.Errorf("%s: the report does not ask for a human: %q", name, changes[0].Reason)
		}
		if got := readString(t, agents); got != seed {
			t.Fatalf("%s: the file was rewritten anyway:\n%s", name, got)
		}
	}
}

// The report drives what a reader does next, so each line has to name the file
// and what happened to it.
func TestEachReportLineNamesTheFileAndTheAction(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, agentsFileName), block("only this")+"\n")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "# mine\n\n"+block("a pointer")+"\n")

	for _, c := range stripDir(dir) {
		line := describeChange(c)
		if !strings.Contains(line, c.Rel) || !strings.Contains(line, c.Action) {
			t.Errorf("report line %q names neither the file nor the action %+v", line, c)
		}
	}
}

// The scan is what `upgrade` and `workspace doctor` report from, so it must find
// exactly what a migration would remove, and it must change nothing.
func TestScanResidueFindsWhatAMigrationRemoves(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, agentsFileName), humanAbove+"\n"+block("generated")+"\n")
	writeFile(t, filepath.Join(dir, "GEMINI.md"), legacyPointerHeader+"\n\nold pointer\n")
	writeFile(t, filepath.Join(dir, ".cursorrules"), "nothing of devstack's here\n")

	found := scanResidue(dir)
	if len(found) != 2 {
		t.Fatalf("scanResidue() found %d files, want 2: %+v", len(found), found)
	}
	for _, f := range found {
		if strings.HasSuffix(f.Path, ".cursorrules") {
			t.Errorf("scanResidue() reported a file with no devstack content: %s", f.Path)
		}
		if f.NeedsHuman {
			t.Errorf("%s has a sound pair of markers, so it needs no human", f.Path)
		}
	}
	if got := readString(t, filepath.Join(dir, agentsFileName)); !strings.Contains(got, "generated") {
		t.Error("the scan changed a file; it must only read")
	}
	if len(scanResidue(dir)) != len(stripDir(dir)) {
		t.Error("the scan and the removal disagree about which files hold devstack content")
	}
}

// A truncated block is what the scan reports as needing a human, so the count in
// the upgrade report matches what the migration refuses to touch.
func TestScanResidueMarksAnUnpairedMarkerForAHuman(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, agentsFileName), humanAbove+"\n"+agentsSentinelBegin+"\ntruncated\n")

	found := scanResidue(dir)
	if len(found) != 1 || !found[0].NeedsHuman {
		t.Fatalf("scanResidue() = %+v, want one file marked for a human", found)
	}
}
