package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// buildSharedRepoStack builds the shape the briefing used to describe wrongly:
// one repository that holds two services, and a stack that overlays one of them.
// git cuts a worktree of a whole repository, so the worktree of the stack holds
// the code of both services, and the stack runs one.
func buildSharedRepoStack(t *testing.T) (*workspace.Workspace, *stack.Record) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "demo")
	gitRepoWith(t, filepath.Join(root, "mono"), map[string]string{"web": "web", "worker": "worker"})
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos:
      - ./mono/web
      - ./mono/worker
`)

	if err := workspace.Register(workspace.Workspace{Name: "demo", Path: root, TiltPort: 10350}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ws, err := workspace.FindByName("demo")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if _, err := stack.Create(stack.CreateInput{Base: ws, Name: "feat", Repos: []string{"web"}}); err != nil {
		t.Fatalf("stack.Create: %v", err)
	}
	rec, err := stack.Resolve("demo", "feat")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return ws, rec
}

// The worktree of a stack holds every service of the repositories it cut, and
// the stack runs only the ones it overlays. The briefing has to know which is
// which before it can say anything true about the directory.
func TestTheBriefingFindsTheServicesTheWorktreeHoldsAndTheStackDoesNotRun(t *testing.T) {
	ws, rec := buildSharedRepoStack(t)
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}

	got := stackSiblings(rw, rec)

	want := filepath.Join(rec.Root, "mono", "worker")
	if got["worker"] != want {
		t.Errorf("stackSiblings() = %v, want worker at %s", got, want)
	}
	if _, ok := got["web"]; ok {
		t.Errorf("web is in the overlay, so the stack runs it: %v", got)
	}
}

// "Every other service is base's copy" is false in a directory the worktree
// holds. The sentence excludes that directory from a prohibition, and an agent
// reads the exclusion as permission: it edits code that no copy runs, the change
// lands on the branch of the stack, and it merges untested.
func TestTheScopeBlockNamesTheCodeTheWorktreeHoldsAndNothingRuns(t *testing.T) {
	ws, rec := buildSharedRepoStack(t)
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}

	var b strings.Builder
	writePrimeStackTask(&b, rec, "web", stackSiblings(rw, rec), false)
	got := b.String()

	for _, want := range []string{
		"worker",
		"runs no copy of them",
		"no process runs it",
		"devstack stack add feat <service>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the task block never states %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Every other service is base's copy") {
		t.Errorf("the worktree holds worker, so this claim is false here:\n%s", got)
	}
}

// The generated manifest of a stack lists the overlay only, so
// config.ResolveIdentity names no service in the directory of a sibling. The
// briefing then dropped the whole THIS SERVICE section for the very code the
// session was looking at, and said nothing at all about the directory.
func TestTheBriefingSaysWhatASiblingDirectoryIs(t *testing.T) {
	ws, rec := buildSharedRepoStack(t)
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	siblings := stackSiblings(rw, rec)

	if id, err := config.ResolveIdentity(siblings["worker"]); err != nil || id.ServiceName != "" {
		t.Fatalf("this test guards the case where the manifest names no service; identity = %+v, err = %v", id, err)
	}

	var b strings.Builder
	writePrimeStackDirectory(&b, rec, "feat", siblings["worker"], siblings)
	got := b.String()

	for _, want := range []string{
		"holds the code of worker",
		"runs no copy of worker",
		"base runs its own copy of worker",
		"runs nowhere",
		"devstack stack add feat worker",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the directory block never states %q:\n%s", want, got)
		}
	}
}

// End to end, from the directory itself: this is what a session gets when it
// starts in the sibling's directory inside a stack worktree.
func TestTheWholeBriefingFromASiblingDirectory(t *testing.T) {
	ws, rec := buildSharedRepoStack(t)
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	t.Chdir(stackSiblings(rw, rec)["worker"])

	got, err := buildPrime()
	if err != nil {
		t.Fatalf("buildPrime() = %v", err)
	}

	for _, want := range []string{
		"## THIS DIRECTORY",
		"holds the code of worker",
		"This stack runs no copy of worker",
		"runs nowhere",
		"devstack stack add feat worker",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the briefing never states %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "## YOUR TASK\nstack feat · web\n") {
		t.Errorf("the briefing must open with the task, and name this stack and its services:\n%s", got)
	}
	for _, want := range []string{
		"devstack service restart web --stack feat",
		`devstack stack note feat --add "what you found"`,
		"The worktrees also hold the code of: worker",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the briefing never states %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Every other service is base's copy") {
		t.Errorf("the directory the session stands in is not base's copy:\n%s", got)
	}
	if n := len(got); n > primeCharBudget {
		t.Errorf("the briefing is %d chars, over the %d budget", n, primeCharBudget)
	}
}

// The close-out of a stack: `stack rm` keeps the branch, so somebody has to
// decide what happens to it, and the agent that closes the stack is not that
// somebody.
func TestTheBriefingCarriesTheCloseOutOfAStack(t *testing.T) {
	var b strings.Builder
	writePrimeCloseOut(&b, "nick/feat")
	got := b.String()

	for _, want := range []string{"Ask the user", "merge this branch, or discard it", "Never merge", "git branch -d nick/feat"} {
		if !strings.Contains(got, want) {
			t.Errorf("the close-out never states %q:\n%s", want, got)
		}
	}
}

// The same close-out, on the command that performs the removal. `stack rm` is
// where an agent arrives with the branch still in its hand.
func TestStackRemoveHelpStatesTheCloseOutOfTheBranch(t *testing.T) {
	got := strings.Join(strings.Fields(stackRemoveCmd.Long), " ")

	for _, want := range []string{
		"Ask the user: merge the branch of this stack, or discard it?",
		"Never merge it without an answer.",
		"git branch -d",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the help of `stack rm` never states %q:\n%s", want, got)
		}
	}
}
