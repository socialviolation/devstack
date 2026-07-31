package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

const abortingDestroyHook = `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./frontend
calls:
  frontend:
    - backend
hooks:
  - name: deprovision
    on: [stack.destroy]
    onError: abort
    run: exit 1
`

// stackWithWorkspaceManifest builds a base workspace from the given manifest and
// creates a real feature stack in it, so a lifecycle command can be run against
// worktrees, ports and a record that all actually exist.
func stackWithWorkspaceManifest(t *testing.T, manifest string) (*workspace.Workspace, *stack.Record) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot := filepath.Join(tmpHome, "dev", "navexa")
	makeStackRepo(t, filepath.Join(baseRoot, "backend"), `version: 1
service:
  name: backend
runtime:
  run:
    command: go run .
ports:
  http: 8080
`)
	makeStackRepo(t, filepath.Join(baseRoot, "frontend"), `version: 1
service:
  name: frontend
runtime:
  run:
    command: npm run dev
`)
	writeFile(t, filepath.Join(baseRoot, "devstack.workspace.yaml"), manifest)

	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: baseRoot, TiltPort: 10350}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	if _, err := stack.Create(stack.CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}}); err != nil {
		t.Fatalf("stack.Create: %v", err)
	}
	rec, err := stack.Resolve("navexa", "feat")
	if err != nil {
		t.Fatalf("resolve stack: %v", err)
	}

	viper.Set("workspace", "navexa")
	t.Cleanup(func() { viper.Set("workspace", "") })
	return base, rec
}

func stackRemoveCommand(t *testing.T, force bool) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "rm"}
	c.Flags().Bool("force", force, "")
	return c
}

// A teardown hook must never make a stack unremovable. onError "abort" on a
// teardown event stops the hooks that follow it, and it does not stop the
// removal — otherwise one broken hook leaks a worktree, a port and a record on
// every attempt.
func TestAnAbortingDestroyHookDoesNotBlockStackRemoval(t *testing.T) {
	base, rec := stackWithWorkspaceManifest(t, abortingDestroyHook)

	if err := runStackRemove(stackRemoveCommand(t, false), []string{"feat"}); err != nil {
		t.Fatalf("runStackRemove() = %v, want the removal to proceed past the failed hook", err)
	}

	stacks, err := stack.List(base.Name)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stacks) != 0 {
		t.Fatalf("stack still registered after rm: %+v", stacks)
	}
	if _, err := os.Stat(rec.Root); !os.IsNotExist(err) {
		t.Fatalf("stack root %s survived the removal", rec.Root)
	}
}

// stack.destroy hooks de-provision state outside this machine, so they must not
// run for a removal that then refuses. A dirty worktree refuses without --force,
// and firing first left the stack alive and already de-provisioned.
func TestADirtyWorktreeRefusesBeforeTheDestroyHookRuns(t *testing.T) {
	base, rec := stackWithWorkspaceManifest(t, hookRecordingDestroy(t))

	dirty := filepath.Join(rec.Root, "backend", "uncommitted.txt")
	writeFile(t, dirty, "work in progress\n")

	err := runStackRemove(stackRemoveCommand(t, false), []string{"feat"})
	if err == nil {
		t.Fatal("runStackRemove() = nil, want a refusal for the dirty worktree")
	}
	if _, statErr := os.Stat(hookMarker); !os.IsNotExist(statErr) {
		t.Fatalf("the stack.destroy hook ran for a removal that refused (marker %s exists)", hookMarker)
	}
	stacks, listErr := stack.List(base.Name)
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(stacks) != 1 {
		t.Fatalf("stacks = %+v, want the stack left registered", stacks)
	}
}

// hookMarker is the file the recording hook touches. It sits outside the stack
// root so the removal cannot delete the evidence.
var hookMarker string

func hookRecordingDestroy(t *testing.T) string {
	t.Helper()
	hookMarker = filepath.Join(t.TempDir(), "destroy-hook-ran")
	return `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./frontend
calls:
  frontend:
    - backend
hooks:
  - name: deprovision
    on: [stack.destroy]
    run: touch ` + hookMarker + `
`
}
