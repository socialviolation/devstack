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

// Staleness is about content, not about which build wrote the file. The stamp
// carries the commit, so every devstack commit changed it — and comparing on it
// made all fifteen repos stale on every release, with a regenerated diff whose
// only change was the stamp recording the regeneration.
func TestStalenessIsDecidedByContentNotVersion(t *testing.T) {
	files := []generatedFile{
		{Service: "same-content-older-stamp", Exists: true, Version: "v0.1.0 (aaaaaaa)", Differs: false},
		{Service: "changed-content", Exists: true, Version: "v0.1.1 (bbbbbbb)", Differs: true},
		{Service: "never-generated", Exists: false, Differs: true},
	}
	names := map[string]bool{}
	for _, f := range staleGenerated(files, "v0.1.1 (bbbbbbb)") {
		names[f.Service] = true
	}

	if names["same-content-older-stamp"] {
		t.Error("an older stamp with identical content needs no regeneration, and reporting it teaches people to ignore the report")
	}
	if !names["changed-content"] {
		t.Error("content this devstack would write differently is exactly what stale means")
	}
	// A repo that never opted into AGENTS.md must not be nagged into one.
	if names["never-generated"] {
		t.Error("a file that does not exist was never asked for, so it is not stale")
	}
}

// The stamp differs between any two builds by design, so it cannot take part in
// the comparison or nothing would ever match.
func TestManagedBodyIgnoresTheStamp(t *testing.T) {
	mk := func(stamp string) string {
		return "# api\n\n" + agentsSentinelBegin + "\n<!-- devstack " + stamp + " · regenerate with `devstack init --all` -->\nthe same instructions\n" + agentsSentinelEnd + "\n"
	}
	if managedBody(mk("v0.1.0 (aaaaaaa)")) != managedBody(mk("v9.9.9 (fffffff)")) {
		t.Error("two builds writing identical instructions must compare equal")
	}
	if got := managedBody(mk("v0.1.0 (aaaaaaa)")); !strings.Contains(got, "the same instructions") {
		t.Errorf("managedBody dropped the instructions it is meant to compare: %q", got)
	}
	if got := managedBody(mk("v0.1.0 (aaaaaaa)")); strings.Contains(got, "devstack v0.1.0") {
		t.Errorf("managedBody kept the stamp: %q", got)
	}
	if got := managedBody("# api\nno managed block here\n"); got != "" {
		t.Errorf("a file with no managed block has no body to compare: %q", got)
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
