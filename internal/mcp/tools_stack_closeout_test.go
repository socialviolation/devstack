package mcp

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/workspace"
)

func TestStackRemoveStatesWhatHappensToTheBranch(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0")
	registerStackRemoveTool(s, &workspace.Workspace{Name: "navexa"})

	got := listTools(t, s)["stack_rm"].Description

	for _, want := range []string{
		"devstack keeps the branch of the stack in git. It does not merge the branch, and it does not delete it.",
		"It refuses a worktree that has uncommitted changes, until you set force.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the stack_rm description never states %q:\n%s", want, got)
		}
	}
	for _, policy := range []string{"ask the user", "NEVER merge", "git branch -d"} {
		if strings.Contains(got, policy) {
			t.Errorf("the stack_rm description prescribes a workflow (%q):\n%s", policy, got)
		}
	}
}
