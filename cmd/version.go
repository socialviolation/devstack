package cmd

import (
	"runtime/debug"
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
