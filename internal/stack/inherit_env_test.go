package stack

import (
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func TestInheritBaseEnvFoldsDefinitionsAndSelection(t *testing.T) {
	worktree := &config.WorkspaceManifest{}
	base := &config.WorkspaceManifest{
		Workspace: config.WorkspaceManifestWorkspace{Env: "dev"},
		Environments: map[string]config.WorkspaceEnvironment{
			"dev":  {},
			"perf": {Values: map[string]string{"FOO": "bar"}},
		},
	}

	inheritBaseEnv(worktree, base)

	svc := &config.ServiceManifest{}
	patch, err := config.ResolveEnvPatch(worktree, svc, "perf")
	if err != nil {
		t.Fatalf("ResolveEnvPatch: %v", err)
	}
	if patch["FOO"] != "bar" {
		t.Errorf("patch[FOO] = %q, want bar", patch["FOO"])
	}

	if worktree.Workspace.Env != "dev" {
		t.Errorf("Workspace.Env = %q, want inherited dev", worktree.Workspace.Env)
	}
}

func TestInheritBaseEnvKeepsWorktreeSelection(t *testing.T) {
	worktree := &config.WorkspaceManifest{
		Workspace: config.WorkspaceManifestWorkspace{Env: "staging"},
	}
	base := &config.WorkspaceManifest{
		Workspace: config.WorkspaceManifestWorkspace{Env: "dev"},
	}

	inheritBaseEnv(worktree, base)

	if worktree.Workspace.Env != "staging" {
		t.Errorf("Workspace.Env = %q, want kept staging", worktree.Workspace.Env)
	}
}
