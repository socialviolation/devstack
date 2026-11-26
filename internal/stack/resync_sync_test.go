package stack

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func addOrigin(t *testing.T, repo string) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), filepath.Base(repo)+".git")
	git(t, repo, "clone", "-q", "--bare", repo, origin)
	git(t, repo, "remote", "add", "origin", origin)
	return origin
}

func pushTo(t *testing.T, origin, branch string, files map[string]string) string {
	t.Helper()
	pod := filepath.Join(t.TempDir(), "pod")
	git(t, filepath.Dir(pod), "clone", "-q", origin, pod)
	if gitOut(t, pod, "ls-remote", "--heads", "origin", branch) == "" {
		git(t, pod, "checkout", "-q", "-B", branch)
	} else {
		git(t, pod, "checkout", "-q", branch)
	}
	for f, content := range files {
		writeFile(t, filepath.Join(pod, f), content)
	}
	git(t, pod, "add", "-f", ".")
	git(t, pod, "commit", "-q", "-m", "pushed")
	git(t, pod, "push", "-q", "origin", "HEAD:"+branch)
	return gitOut(t, pod, "rev-parse", "HEAD")
}

func newDetachedResyncStack(t *testing.T) (base *workspace.Workspace, apiWT, apiOrigin string) {
	t.Helper()
	base = newAddBase(t)
	created := newStack(t, base, "web")
	apiOrigin = addOrigin(t, filepath.Join(base.Path, "api"))
	if err := SetActive(base.Name, "feat", true); err != nil {
		t.Fatal(err)
	}
	return base, worktreesByService(created)["api"].Path, apiOrigin
}

func TestResyncMovesADetachedWorktreeToTheDefaultTip(t *testing.T) {
	base, apiWT, origin := newDetachedResyncStack(t)
	old := gitOut(t, apiWT, "rev-parse", "HEAD")
	tip := pushTo(t, origin, "main", map[string]string{"new.txt": "on main\n"})
	var f fakeRestart

	res, err := Resync(base, "feat", f.restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	api := repoNamed(t, res, "api")
	if api.Status != ResyncUpdated || api.Old != old || api.New != tip || api.Branch != "main" {
		t.Errorf("api = %+v, want updated %s -> %s on main", api, old, tip)
	}
	if got := gitOut(t, apiWT, "rev-parse", "HEAD"); got != tip {
		t.Errorf("api worktree at %s, want %s", got, tip)
	}
	if got := gitOut(t, apiWT, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("api worktree on %s, want detached", got)
	}
	want := []string{"api", "web"}
	if !reflect.DeepEqual(res.Restart, want) || !reflect.DeepEqual(f.calls, [][]string{want}) {
		t.Errorf("Restart = %v, calls = %v, want %v", res.Restart, f.calls, want)
	}
}

func TestResyncLeavesADetachedWorktreeAtTheTip(t *testing.T) {
	base, _, _ := newDetachedResyncStack(t)

	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if api := repoNamed(t, res, "api"); api.Status != ResyncUnchanged || api.Old != api.New {
		t.Errorf("api = %+v, want unchanged", api)
	}
	if len(res.Restart) != 0 {
		t.Errorf("Restart = %v, want none", res.Restart)
	}
}

func TestResyncRefusesADirtyDetachedWorktree(t *testing.T) {
	base, apiWT, origin := newDetachedResyncStack(t)
	old := gitOut(t, apiWT, "rev-parse", "HEAD")
	pushTo(t, origin, "main", map[string]string{"new.txt": "on main\n"})
	writeFile(t, filepath.Join(apiWT, "wip.go"), "package main\n")
	var f fakeRestart

	res, err := Resync(base, "feat", f.restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	api := repoNamed(t, res, "api")
	if api.Status != ResyncRefused || !strings.Contains(api.Reason, "uncommitted changes: wip.go") {
		t.Errorf("api = %+v, want refused naming wip.go", api)
	}
	if got := gitOut(t, apiWT, "rev-parse", "HEAD"); got != old {
		t.Errorf("api worktree moved to %s", got)
	}
	if len(f.calls) != 0 {
		t.Errorf("restarted %v after a refusal", f.calls)
	}
}

func TestResyncMovesADetachModeStack(t *testing.T) {
	base := newAddBase(t)
	noDaemon(t)
	created, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"api"}, Detach: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	origin := addOrigin(t, filepath.Join(base.Path, "api"))
	addOrigin(t, filepath.Join(base.Path, "billing"))
	tip := pushTo(t, origin, "main", map[string]string{"new.txt": "on main\n"})

	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if api := repoNamed(t, res, "api"); api.Status != ResyncUpdated || api.New != tip {
		t.Errorf("api = %+v, want updated to %s", api, tip)
	}
	if got := gitOut(t, worktreesByService(created)["api"].Path, "rev-parse", "HEAD"); got != tip {
		t.Errorf("api worktree at %s, want %s", got, tip)
	}
	if !res.Down || len(res.Outside) != 0 || len(res.Notes) != 0 {
		t.Errorf("Down = %v, Outside = %v, Notes = %v, want down and no reports", res.Down, res.Outside, res.Notes)
	}
}

func TestResyncReportsTheBranchInARepoOutsideTheStack(t *testing.T) {
	base, _, origin := newResyncStack(t)
	pushFeat(t, origin, "pushed.txt")
	billing := addOrigin(t, filepath.Join(base.Path, "billing"))
	pushTo(t, billing, "feat", map[string]string{"b.txt": "billing\n"})
	addOrigin(t, filepath.Join(base.Path, "web"))

	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	want := []OutsideRepo{{Repo: "billing", Services: []string{"billing"}}}
	if !reflect.DeepEqual(res.Outside, want) {
		t.Errorf("Outside = %+v, want %+v", res.Outside, want)
	}
	if len(res.Notes) != 0 {
		t.Errorf("Notes = %v, want none", res.Notes)
	}
}

func TestResyncSkipsARepoTheStackRunsAServiceFrom(t *testing.T) {
	base := newMonoBase(t)
	noDaemon(t)
	created, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"agent-rag"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	addOrigin(t, filepath.Join(base.Path, "mono"))
	git(t, worktreesByService(created)["agent-rag"].RepoPath, "push", "-q", "origin", "feat")
	backend := addOrigin(t, filepath.Join(base.Path, "backend"))
	pushTo(t, backend, "feat", map[string]string{"b.txt": "backend\n"})

	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	want := []OutsideRepo{{Repo: "backend", Services: []string{"backend"}}}
	if !reflect.DeepEqual(res.Outside, want) {
		t.Errorf("Outside = %+v, want %+v", res.Outside, want)
	}
}

func TestResyncNotesAFailingOriginAndGoesOn(t *testing.T) {
	base, _, origin := newResyncStack(t)
	tip := pushFeat(t, origin, "pushed.txt")
	git(t, filepath.Join(base.Path, "billing"), "remote", "add", "origin", filepath.Join(t.TempDir(), "gone.git"))

	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if api := repoNamed(t, res, "api"); api.New != tip {
		t.Errorf("api = %+v, want updated to %s", api, tip)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "billing") || len(res.Outside) != 0 {
		t.Errorf("Notes = %v, Outside = %v, want one note on billing", res.Notes, res.Outside)
	}
}

func TestResyncReportsANewNeed(t *testing.T) {
	base, _, origin := newResyncStack(t)
	pushTo(t, origin, "feat", map[string]string{"devstack.service.yaml": apiSvc + "needs:\n  - billing\n"})

	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	want := []OverlayMember{{Service: "billing", Reason: ReasonNeeds, Chain: []string{"api", "billing"}}}
	if !reflect.DeepEqual(res.Missing, want) {
		t.Errorf("Missing = %+v, want %+v", res.Missing, want)
	}
}

func TestResyncWarnsOnlyWhenCommitsTouchMigrations(t *testing.T) {
	base, _, origin := newResyncStack(t)
	pushTo(t, origin, "feat", map[string]string{"devstack.service.yaml": apiSvc + "migrations: db/migrations\n"})
	if res, err := Resync(base, "feat", (&fakeRestart{}).restart); err != nil || len(res.Migrations) != 0 {
		t.Fatalf("Resync = %+v, %v, want no migration warning for a manifest change", res, err)
	}

	pushTo(t, origin, "feat", map[string]string{"other/x.txt": "not a migration\n", "db/migrationsX.txt": "a sibling\n"})
	res, err := Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil || repoNamed(t, res, "api").Status != ResyncUpdated || len(res.Migrations) != 0 {
		t.Fatalf("Resync = %+v, %v, want an update and no migration warning", res, err)
	}

	pushTo(t, origin, "feat", map[string]string{"db/migrations/002.sql": "create table x();\n"})
	res, err = Resync(base, "feat", (&fakeRestart{}).restart)
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if !reflect.DeepEqual(res.Migrations, []string{"api"}) {
		t.Errorf("Migrations = %v, want [api]", res.Migrations)
	}
}

func TestBehindWarningsNamesTheBranchAndTheDetachedRepo(t *testing.T) {
	base, _, apiOrigin := newDetachedResyncStack(t)
	rec, err := Resolve(base.Name, "feat")
	if err != nil {
		t.Fatal(err)
	}
	webOrigin := addOrigin(t, filepath.Join(base.Path, "web"))
	git(t, rec.Worktrees["web"], "push", "-q", "origin", "feat")

	if w, n := BehindWarnings(rec); len(w) != 0 || len(n) != 0 {
		t.Fatalf("warnings = %v, notes = %v, want none before a push", w, n)
	}

	pushTo(t, webOrigin, "feat", map[string]string{"a.txt": "1\n"})
	pushTo(t, webOrigin, "feat", map[string]string{"b.txt": "2\n"})
	pushTo(t, apiOrigin, "main", map[string]string{"c.txt": "3\n"})

	w, n := BehindWarnings(rec)
	want := []string{
		"api (detached, against the default branch main) is behind origin by 1 commit. Run `stack_resync`.",
		"web (on branch feat) is behind origin by 2 commits. Run `stack_resync`.",
	}
	if !reflect.DeepEqual(w, want) || len(n) != 0 {
		t.Errorf("warnings = %q, notes = %v, want %q", w, n, want)
	}
}

func TestBehindWarningsNotesAFailingOrigin(t *testing.T) {
	base, _, _ := newDetachedResyncStack(t)
	rec, err := Resolve(base.Name, "feat")
	if err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Join(base.Path, "web"), "remote", "add", "origin", filepath.Join(t.TempDir(), "gone.git"))

	w, n := BehindWarnings(rec)
	if len(w) != 0 || len(n) != 1 || !strings.Contains(n[0], "web") {
		t.Errorf("warnings = %v, notes = %v, want one note on web", w, n)
	}
}
