package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A stack's worktrees live outside its base workspace directory and no stack is
// registered, so workspace.DetectFromCwd cannot place a caller standing in one.
// resolveWorkspace is what adds the fallback to the owning base. Calling
// DetectFromCwd directly skips it, and every command that did failed in the one
// directory agents are sent to work in — `devstack otel traces` still answered
// "not inside a registered devstack workspace. Run: devstack workspace add"
// there, after `prime` had told the agent to go there and query its telemetry.
//
// resolveWorkspace itself is the single permitted caller.
func TestCommandsResolveWorkspaceThroughTheStackAwarePath(t *testing.T) {
	const permitted = "start_cmd.go"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "workspace.DetectFromCwd()") {
				continue
			}
			found = true
			if name != permitted {
				t.Errorf("%s:%d calls workspace.DetectFromCwd() directly, so it fails inside a stack worktree — call resolveWorkspace(\"\") instead: %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if !found {
		t.Fatalf("no call to workspace.DetectFromCwd() found in %s — this guard checks nothing", permitted)
	}
}
