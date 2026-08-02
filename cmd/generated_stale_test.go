package cmd

import (
	"strings"
	"testing"
)

// The stamp is read back out of files devstack wrote earlier, so the two halves
// have to agree. Writing one format and parsing another would report every file
// as unstamped, and every workspace as needing migration for ever.
func TestProvenanceRoundTripsThroughItsOwnParser(t *testing.T) {
	line := agentsProvenance()
	m := provenanceRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("agentsProvenance() writes a line its own parser does not match: %q", line)
	}
	if m[1] != buildStamp() {
		t.Errorf("parsed stamp = %q, want %q", m[1], buildStamp())
	}
}

// buildVersion is the version alone and must stay a single token: prose in it
// would be stamped into files and parsed back as a version.
func TestBuildVersionIsASingleToken(t *testing.T) {
	if got := buildVersion(); strings.ContainsAny(got, " \t") {
		t.Errorf("buildVersion() = %q, which must be one token", got)
	}
}

// Go synthesises a version for any build not exactly at a semver tag, and it
// reads as a version while being a timestamp: v0.1.1-0.20260801235720-dc86c8e67eaa
// a commit after v0.1.0, v0.0.0-<stamp>-<sha> with no tag at all. Neither is
// shown to anybody. The commit is carried separately, where it is useful.
func TestSynthesisedVersionsAreNotShownAsVersions(t *testing.T) {
	for _, v := range []string{
		"v0.0.0-20260801235720-dc86c8e67eaa",
		"v0.1.1-0.20260801235720-dc86c8e67eaa",
		"v0.0.0-20260801235720-dc86c8e67eaa+dirty",
	} {
		if !pseudoVersion.MatchString(strings.TrimSuffix(v, "+dirty")) {
			t.Errorf("%q is a synthesised version and must not be printed as one", v)
		}
	}
	for _, v := range []string{"v0.1.0", "v1.2.3", "v0.1.0-rc1"} {
		if pseudoVersion.MatchString(v) {
			t.Errorf("%q is a real tag and must be shown as the version", v)
		}
	}
}

// The stamp carries the commit beside the version, so a build between tags is
// still identifiable and a later devstack can compare exactly.
func TestStampCarriesTheCommit(t *testing.T) {
	if sha := buildSHA(); sha != "" {
		if got := buildStamp(); !strings.Contains(got, sha) {
			t.Errorf("buildStamp() = %q, want it to carry the commit %q", got, sha)
		}
	}
}

func TestStaleGeneratedPicksOnlyFilesAnotherVersionWrote(t *testing.T) {
	files := []generatedFile{
		{Service: "current", Exists: true, Version: "v2"},
		{Service: "older", Exists: true, Version: "v1"},
		{Service: "unstamped", Exists: true, Version: ""},
		{Service: "never-generated", Exists: false},
	}
	got := staleGenerated(files, "v2")

	names := map[string]bool{}
	for _, f := range got {
		names[f.Service] = true
	}
	if !names["older"] || !names["unstamped"] {
		t.Errorf("a file written by another version, or written before the stamp, is stale: %v", names)
	}
	if names["current"] {
		t.Error("a file this version wrote is not stale")
	}
	// A repo that never opted into AGENTS.md must not be nagged into one.
	if names["never-generated"] {
		t.Error("a file that does not exist was never asked for, so it is not stale")
	}
}

// The marker is what the parser anchors on. Rewording the advice after it must
// not orphan every file already committed.
func TestProvenanceParsesRegardlessOfTrailingAdvice(t *testing.T) {
	for _, line := range []string{
		"<!-- devstack v1.2.3 · regenerate with `devstack init --all` -->",
		"<!-- devstack v1.2.3 · some entirely different advice -->",
		"<!-- devstack v1.2.3 -->",
	} {
		m := provenanceRe.FindStringSubmatch(line)
		if m == nil || m[1] != "v1.2.3" {
			t.Errorf("parsing %q gave %v, want v1.2.3", line, m)
		}
	}
}
