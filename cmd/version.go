package cmd

import (
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/socialviolation/devstack/internal/selfcheck"
)

// buildVersion identifies the running binary from the build information Go
// embeds by itself. No -ldflags are involved: a build from a git checkout stamps
// the commit, its time and whether the tree was dirty, and `go install` carries
// them into the installed binary.
//
// A binary that cannot say what it is cannot be told apart from a stale one, and
// the CLI is noun-first with no aliases — an old binary answers a current
// command with "unknown command" and names no cause. This is what the answer
// costs: one call.
// It returns a bare token and never prose, because the stamp is compared back
// out of generated files. Explanation belongs in versionLine, which nothing
// parses.
//
// Only a real semver tag is reported as a version. Go synthesises a version for
// every other build — v0.1.1-0.20260801235720-dc86c8e67eaa a commit after
// v0.1.0, v0.0.0-<timestamp>-<sha> with no tag at all — which reads as a version
// while being a timestamp, and buries the one part anybody wants. Those are
// "dev", and the commit is carried beside it where a reader can use it.
//
// Note for tagging: the tag must be plain semver, and major versions above 1
// need a matching /vN module path. Calendar tags like 2026.08-1 or v2026.8.1 are
// both rejected, and a rejected tag is silently ignored rather than reported.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return devVersion
	}
	v := strings.TrimSuffix(info.Main.Version, "+dirty")
	if v == "" || v == "(devel)" || pseudoVersion.MatchString(v) {
		return devVersion
	}
	return v
}

// devVersion is what a build that carries no semver tag calls itself: a build
// from source, and never a published release.
const devVersion = "dev"

// pseudoVersion matches the version Go synthesises when a build is not exactly
// at a semver tag: a 14-digit timestamp and a 12-character commit. The separator
// before the timestamp is a dot in the pre-release form Go uses after a tag
// (v0.1.1-0.20260801235720-dc86c8e), and a dash when there is no tag at all.
var pseudoVersion = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}$`)

// buildSHA is the short commit this binary was built from. It is the part of a
// synthesised version worth keeping, so it is kept on its own rather than inside
// a string pretending to be a version.
func buildSHA() string {
	rev := buildRevision()
	if len(rev) < 7 {
		return rev
	}
	return rev[:7]
}

// buildStamp is how devstack names itself everywhere a human or a later devstack
// will read it: the version, then the commit, then whether the tree was dirty.
func buildStamp() string {
	v := buildVersion()
	sha := buildSHA()
	if sha == "" {
		return v
	}
	if buildDirty() {
		return v + " (" + sha + ", uncommitted changes)"
	}
	return v + " (" + sha + ")"
}

func buildDirty() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.modified" {
			return s.Value == "true"
		}
	}
	return false
}

// buildRevision is the commit this binary was built from, and the key the update
// check compares and caches against.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

// modulePath is the import path this binary was built from, so the update check
// and the install command it prints are derived rather than hardcoded.
func modulePath() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Path
}

// versionLine is what `devstack --version` prints. It reads the cached update
// result and never the network: this runs during command setup, and no command
// should wait on a network to say its own name. `devstack prime` is what
// refreshes the cache, and it runs at the start of every session.
func versionLine() string {
	v := buildStamp()
	if r, ok := selfcheck.Cached(buildRevision()); ok {
		if line := r.Describe(modulePath()); line != "" {
			return v + "\n  " + line
		}
	}
	return v
}
