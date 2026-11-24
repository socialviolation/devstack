package stack

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func newResyncStack(t *testing.T) (base *workspace.Workspace, apiWT, origin string) {
	t.Helper()
	base = newAddBase(t)
	created := newStack(t, base, "api")
	origin = filepath.Join(t.TempDir(), "api.git")
	apiRepo := filepath.Join(base.Path, "api")
	git(t, apiRepo, "clone", "-q", "--bare", apiRepo, origin)
	git(t, origin, "branch", "-q", "-D", "feat")
	git(t, apiRepo, "remote", "add", "origin", origin)
	if err := SetActive(base.Name, "feat", true); err != nil {
		t.Fatal(err)
	}
	return base, worktreesByService(created)["api"].Path, origin
}

func pushFeat(t *testing.T, origin, file string) string {
	t.Helper()
	pod := filepath.Join(t.TempDir(), "pod")
	git(t, filepath.Dir(pod), "clone", "-q", origin, pod)
	git(t, pod, "checkout", "-q", "-b", "feat")
	writeFile(t, filepath.Join(pod, file), "from the pod\n")
	git(t, pod, "add", "-f", ".")
	git(t, pod, "commit", "-q", "-m", "pushed")
	git(t, pod, "push", "-q", "origin", "feat")
	return gitOut(t, pod, "rev-parse", "HEAD")
}

type fakeRestart struct{ calls [][]string }

func (f *fakeRestart) restart(rec *Record, services []string) ([]string, error) {
	f.calls = append(f.calls, services)
	return services, nil
}

func repoNamed(t *testing.T, res *ResyncResult, name string) RepoResync {
	t.Helper()
	for _, r := range res.Repos {
		if r.Repo == name {
			return r
		}
	}
	t.Fatalf("no repo %q in %+v", name, res.Repos)
	return RepoResync{}
}

func TestResyncFastForwardsAndRestartsDependents(t *testing.T) {
	base, apiWT, origin := newResyncStack(t)
	old := gitOut(t, apiWT, "rev-parse", "HEAD")
	tip := pushFeat(t, origin, "pushed.txt")
	var f fakeRestart

	res, err := Resync(base, "feat", f.restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	api := repoNamed(t, res, "api")
	if api.Status != ResyncUpdated || api.Old != old || api.New != tip || api.Branch != "feat" {
		t.Errorf("api = %+v, want updated %s -> %s on feat", api, old, tip)
	}
	if got := gitOut(t, apiWT, "rev-parse", "HEAD"); got != tip {
		t.Errorf("api worktree at %s, want %s", got, tip)
	}
	if web := repoNamed(t, res, "web"); web.Status != ResyncUnchanged || !strings.Contains(web.Reason, "detached caller") {
		t.Errorf("web = %+v, want unchanged as a detached caller", web)
	}
	want := []string{"api", "web"}
	if !reflect.DeepEqual(res.Restart, want) || !reflect.DeepEqual(f.calls, [][]string{want}) || !reflect.DeepEqual(res.Restarted, want) {
		t.Errorf("Restart = %v, calls = %v, Restarted = %v, want %v", res.Restart, f.calls, res.Restarted, want)
	}
}

func TestResyncDownStackRestartsNothing(t *testing.T) {
	base, _, origin := newResyncStack(t)
	if err := SetActive(base.Name, "feat", false); err != nil {
		t.Fatal(err)
	}
	pushFeat(t, origin, "pushed.txt")
	var f fakeRestart

	res, err := Resync(base, "feat", f.restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if !res.Down || len(f.calls) != 0 || repoNamed(t, res, "api").Status != ResyncUpdated {
		t.Errorf("Down = %v, calls = %v, repos = %+v, want an update and no restart", res.Down, f.calls, res.Repos)
	}
}

func TestResyncUnchangedRepoRestartsNothing(t *testing.T) {
	base, apiWT, _ := newResyncStack(t)
	git(t, apiWT, "push", "-q", "origin", "feat")
	var f fakeRestart

	res, err := Resync(base, "feat", f.restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if api := repoNamed(t, res, "api"); api.Status != ResyncUnchanged || api.Old != api.New {
		t.Errorf("api = %+v, want unchanged", api)
	}
	if len(res.Restart) != 0 || len(f.calls) != 0 {
		t.Errorf("Restart = %v, calls = %v, want none", res.Restart, f.calls)
	}
}

func TestResyncRefusesADirtyWorktree(t *testing.T) {
	base, apiWT, origin := newResyncStack(t)
	pushFeat(t, origin, "pushed.txt")
	writeFile(t, filepath.Join(apiWT, "wip.go"), "package main\n")
	var f fakeRestart

	res, err := Resync(base, "feat", f.restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	api := repoNamed(t, res, "api")
	if api.Status != ResyncRefused || !strings.Contains(api.Reason, "uncommitted changes: wip.go") || api.Old != api.New {
		t.Errorf("api = %+v, want refused naming wip.go", api)
	}
	if len(f.calls) != 0 {
		t.Errorf("restarted %v after a refusal", f.calls)
	}
}

func TestResyncRefusesDivergedBranches(t *testing.T) {
	base, apiWT, origin := newResyncStack(t)
	remote := pushFeat(t, origin, "pushed.txt")
	writeFile(t, filepath.Join(apiWT, "local.txt"), "local\n")
	git(t, apiWT, "add", "-f", ".")
	git(t, apiWT, "commit", "-q", "-m", "local")
	local := gitOut(t, apiWT, "rev-parse", "HEAD")

	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	api := repoNamed(t, res, "api")
	if api.Status != ResyncRefused || !strings.Contains(api.Reason, "diverged") ||
		!strings.Contains(api.Reason, local) || !strings.Contains(api.Reason, remote) {
		t.Errorf("api = %+v, want a divergence refusal naming %s and %s", api, local, remote)
	}
	if got := gitOut(t, apiWT, "rev-parse", "HEAD"); got != local {
		t.Errorf("api worktree moved to %s", got)
	}
}

func TestResyncReportsAMissingRemoteBranch(t *testing.T) {
	base, _, _ := newResyncStack(t)

	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if api := repoNamed(t, res, "api"); api.Status != ResyncRefused || !strings.Contains(api.Reason, "origin has no branch feat") {
		t.Errorf("api = %+v, want a missing remote branch refusal", api)
	}
}
