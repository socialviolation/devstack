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
	if m[1] != buildVersion() {
		t.Errorf("parsed version = %q, want %q", m[1], buildVersion())
	}
}

// buildVersion is stamped into files and compared back out, so it has to stay a
// single token. Prose in it would be parsed as a version and never match again.
func TestBuildVersionIsASingleToken(t *testing.T) {
	if got := buildVersion(); strings.ContainsAny(got, " \t") {
		t.Errorf("buildVersion() = %q, which is parsed back out of generated files and must be one token", got)
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
