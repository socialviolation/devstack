package cmd

import (
	"runtime/debug"

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
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown — this binary carries no build information"
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		return "devel — built outside a module, so no commit is recorded"
	}
	return v
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
	v := buildVersion()
	if r, ok := selfcheck.Cached(buildRevision()); ok {
		if line := r.Describe(modulePath()); line != "" {
			return v + "\n  " + line
		}
	}
	return v
}
