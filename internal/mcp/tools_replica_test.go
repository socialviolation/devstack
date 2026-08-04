package mcp

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

// baseTermDesc is pasted into every description that says "base", so it is the
// one place the replica has to be stated. A definition that still calls base
// "the normal checkouts" tells an agent to edit a directory nothing runs.
func TestBaseIsDefinedAsTheReplicaEverywhereItIsUsed(t *testing.T) {
	if !strings.Contains(baseTermDesc, "replica") || !strings.Contains(baseTermDesc, "template") {
		t.Fatalf("baseTermDesc must define base as a replica built from the checkout: %s", baseTermDesc)
	}
	if strings.Contains(baseTermDesc, "the normal checkouts and the service copies started from them") {
		t.Fatalf("baseTermDesc still says base runs from the checkouts: %s", baseTermDesc)
	}
}

// The first tool an agent calls has to carry both rules, because an agent that
// stops there still acts.
func TestEnvironmentToolOrientsOnTheReplicaAndTheTargetRule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, root := clarityWorkspace(t)

	s := server.NewMCPServer("test", "0.0.0")
	registerEnvironmentTool(s, "http://localhost:5080", ws.Name, root, "", ws)

	out := clarityCallTool(t, s, "environment")
	for _, want := range []string{"replica", "template", "no default copy"} {
		if !strings.Contains(out, want) {
			t.Errorf("environment must orient on %q; got %s", want, out)
		}
	}
}

// stack_create no longer cuts from what the checkout has checked out, and an
// agent told otherwise reports a stack that contains work it does not.
func TestStackCreateSaysWhereWorktreesAreCutFrom(t *testing.T) {
	tools := listEnvStackTools(t)
	desc := tools["stack_create"].Description
	for _, want := range []string{"DEFAULT BRANCH", "not from whatever the user's checkout has checked out"} {
		if !strings.Contains(desc, want) {
			t.Errorf("stack_create must state where a worktree is cut from (%q): %s", want, desc)
		}
	}
	if from := tools["stack_create"].InputSchema.Properties["from"].Description; !strings.Contains(from, "default branch") {
		t.Errorf("the from parameter must state its default: %s", from)
	}
}

// service_env writes files in a checkout. For base that checkout is the
// template, so a write there does not reach the running copy on a restart alone
// — the description has to say what does.
func TestServiceEnvSaysABaseWriteNeedsASync(t *testing.T) {
	tool := listEnvStackTools(t)["service_env"]
	if !strings.Contains(tool.Description, "devstack workspace up") {
		t.Errorf("service_env must say how a base write reaches the running copy: %s", tool.Description)
	}
	if desc := tool.InputSchema.Properties["stack"].Description; !strings.Contains(desc, "template") {
		t.Errorf("the stack parameter must name the base checkout as the template: %s", desc)
	}
}

// env_use changes what a scope runs with, so it takes the same explicit target
// as the other mutating tools: omitting it is not the workspace default.
func TestEnvUseDescriptionDropsTheWorkspaceDefault(t *testing.T) {
	tools := listEnvStackTools(t)
	desc := tools["env_use"].Description
	if strings.Contains(desc, "with neither service nor stack devstack sets the workspace default") {
		t.Errorf("env_use still claims an implicit workspace scope: %s", desc)
	}
	if !strings.Contains(desc, "stack='base' sets the workspace default") {
		t.Errorf("env_use must say how the workspace scope is named now: %s", desc)
	}
}

// status reports the directory a service's config was read from. For base that
// is the checkout while the process runs the replica, so the description must
// not present it as the code that is executing.
func TestStatusDescriptionDoesNotCallTheCheckoutTheRunningCode(t *testing.T) {
	desc := listCoreTools(t)["status"].Description
	if strings.Contains(desc, "this is the code the process is running") {
		t.Errorf("status still claims the checkout's branch is what base executes: %s", desc)
	}
	if !strings.Contains(desc, "base runs a replica") {
		t.Errorf("status must say a base row's PATH is not what base executes: %s", desc)
	}
}
