package cmd

import (
	"strings"
	"testing"
)

// buildVersion is the version alone and must stay a single token: `upgrade`
// reads the stamp back out of `devstack --version`, so prose in it would be
// parsed as a version.
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
