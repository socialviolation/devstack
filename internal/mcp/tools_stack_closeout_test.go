package mcp

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/workspace"
)

// stack_rm deletes the worktrees and keeps the branch, so the session that
// closes a stack meets a decision: merge the work, or throw it away. It is not
// the session's decision to make, and the tool that removes the stack is where
// the agent reads that.
func TestStackRemoveStatesTheCloseOutOfTheBranch(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0")
	registerStackRemoveTool(s, &workspace.Workspace{Name: "navexa"})

	got := listTools(t, s)["stack_rm"].Description

	for _, want := range []string{
		"ask the user",
		"merge the branch of this stack, or discard it",
		"NEVER merge it without an answer",
		"git branch -d",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the stack_rm description never states %q:\n%s", want, got)
		}
	}
}
