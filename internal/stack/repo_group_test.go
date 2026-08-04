package stack

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func svcManifest(name string) string {
	return "version: 1\nservice:\n  name: " + name + "\nruntime:\n  run:\n    command: go run .\nports:\n  http: 8080\n"
}

// makeMultiServiceRepo builds one repository that holds several services in
// subdirectories.
func makeMultiServiceRepo(t *testing.T, dir string, services ...string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "README.md"), "the repository root\n")
	for _, s := range services {
		writeFile(t, filepath.Join(dir, s, "devstack.service.yaml"), svcManifest(s))
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-f", ".")
	git(t, dir, "commit", "-q", "-m", "init")
}

// newMonoBase is a workspace whose repository "mono" holds two services in
// subdirectories, next to a service that is the root of its own repository.
func newMonoBase(t *testing.T) *workspace.Workspace {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot := filepath.Join(tmpHome, "dev", "navexa")
	makeRepo(t, filepath.Join(baseRoot, "backend"), backendSvc)
	makeMultiServiceRepo(t, filepath.Join(baseRoot, "mono"), "agent-rag", "agent-psql")

	writeFile(t, filepath.Join(baseRoot, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./backend
      - ./mono/agent-rag
      - ./mono/agent-psql
`)
	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: baseRoot, TiltPort: 10350}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	return base
}

func dirsUnder(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// git cuts a worktree of a whole repository and never of a subdirectory. A
// stack that overlays a service in a subdirectory must therefore cut ONE
// worktree of the repository and point the service at its directory in it.
func TestCreateCutsOneWorktreeForARepositoryWithTwoServices(t *testing.T) {
	base := newMonoBase(t)
	noDaemon(t)

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"agent-rag"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := dirsUnder(t, res.StackRoot); !reflect.DeepEqual(got, []string{"mono"}) {
		t.Fatalf("stack root holds %v, want one directory named after the repository", got)
	}
	if len(res.Worktrees) != 1 {
		t.Fatalf("Worktrees = %+v, want one entry for the one overlay service", res.Worktrees)
	}
	wt := res.Worktrees[0]
	if wt.Service != "agent-rag" {
		t.Fatalf("Worktrees[0].Service = %q, want agent-rag", wt.Service)
	}
	if wt.Repo != "mono" || wt.RepoPath != filepath.Join(res.StackRoot, "mono") {
		t.Errorf("Repo/RepoPath = %q/%q, want mono at %s", wt.Repo, wt.RepoPath, filepath.Join(res.StackRoot, "mono"))
	}
	if wt.Path != filepath.Join(res.StackRoot, "mono", "agent-rag") {
		t.Errorf("Path = %q, want the service directory in the repository worktree", wt.Path)
	}
	if wt.Branch != "feat" || wt.Detached {
		t.Errorf("worktree = %+v, want the stack's branch feat", wt)
	}
	if got := gitOut(t, wt.RepoPath, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat" {
		t.Errorf("repository worktree branch = %q, want feat", got)
	}

	rw, err := config.ResolveWorkspace(res.StackRoot)
	if err != nil {
		t.Fatalf("resolve the generated manifest: %v", err)
	}
	if len(rw.Services) != 1 {
		t.Fatalf("stack manifest services = %v, want only agent-rag", rw.Services)
	}
	if got := rw.Services["agent-rag"].RepoPath; got != wt.Path {
		t.Errorf("manifest RepoPath = %q, want %q", got, wt.Path)
	}

	// The sibling's code is in the worktree, because a repository has one
	// checkout. The stack still does not run it, and the output must say so.
	if _, err := os.Stat(filepath.Join(wt.RepoPath, "agent-psql", "devstack.service.yaml")); err != nil {
		t.Errorf("the sibling service's code is missing from the worktree: %v", err)
	}
	var shared string
	for _, w := range res.Warnings {
		if strings.Contains(w, "agent-psql") {
			shared = w
		}
	}
	if shared == "" {
		t.Fatalf("no warning that agent-psql shares the repository and stays on base: %v", res.Warnings)
	}
	for _, want := range []string{"mono", "Base runs them", "devstack stack add feat agent-psql"} {
		if !strings.Contains(shared, want) {
			t.Errorf("warning must contain %q, got: %s", want, shared)
		}
	}

	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if !reflect.DeepEqual(rec.Overlay, []string{"agent-rag"}) {
		t.Errorf("record overlay = %v, want only the service that was named", rec.Overlay)
	}

	// One worktree, so one removal. Removing it once for each service failed on
	// the second attempt.
	rm, err := Remove(base, "feat", false)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(rm.RemovedWorktrees) != 1 {
		t.Errorf("removed %v, want one worktree for the one repository", rm.RemovedWorktrees)
	}
}

// Adding the sibling must reuse the worktree the repository already has. A
// second worktree of the same repository is what git refuses.
func TestAddReusesTheWorktreeOfARepositoryTheStackAlreadyHas(t *testing.T) {
	base := newMonoBase(t)
	created := newStack(t, base, "agent-rag")

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"agent-psql"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := dirsUnder(t, created.StackRoot); !reflect.DeepEqual(got, []string{"mono"}) {
		t.Fatalf("stack root holds %v, want the one repository worktree", got)
	}
	if len(res.Worktrees) != 1 || res.Worktrees[0].Service != "agent-psql" {
		t.Fatalf("Worktrees = %+v, want agent-psql", res.Worktrees)
	}
	want := filepath.Join(created.StackRoot, "mono", "agent-psql")
	if res.Worktrees[0].Path != want {
		t.Errorf("Path = %q, want %q", res.Worktrees[0].Path, want)
	}
	if res.Worktrees[0].Branch != "feat" {
		t.Errorf("Branch = %q, want feat: the repository worktree is on the branch already", res.Worktrees[0].Branch)
	}

	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if rec.Worktrees["agent-psql"] != want {
		t.Errorf("record worktree = %q, want %q", rec.Worktrees["agent-psql"], want)
	}
	rw, err := config.ResolveWorkspace(rec.Root)
	if err != nil {
		t.Fatalf("resolve the regenerated manifest: %v", err)
	}
	if len(rw.Services) != 2 {
		t.Errorf("stack manifest services = %v, want both services of the repository", rw.Services)
	}
	if rw.Services["agent-psql"].RepoPath != want {
		t.Errorf("manifest RepoPath = %q, want %q", rw.Services["agent-psql"].RepoPath, want)
	}
}

// Two workspaces can share a parent directory. A stack root that leaves the
// workspace name out gives both the same path, and the second create dies on
// "already exists".
func TestTwoWorkspacesSharingAParentBothCreateAStackOfTheSameName(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	noDaemon(t)

	makeWS := func(name, dir string) *workspace.Workspace {
		root := filepath.Join(tmpHome, "dev", dir)
		makeRepo(t, filepath.Join(root, "backend"), backendSvc)
		writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), "version: 1\nworkspace:\n  name: "+name+"\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./backend\n")
		if err := workspace.Register(workspace.Workspace{Name: name, Path: root, TiltPort: 10350}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		ws, err := workspace.FindByName(name)
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		return ws
	}
	navexa := makeWS("navexa", "navexa")
	southfoundry := makeWS("southfoundry", "tsfc")

	first, err := Create(CreateInput{Base: navexa, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create in navexa: %v", err)
	}
	second, err := Create(CreateInput{Base: southfoundry, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create in southfoundry with a name navexa already uses: %v", err)
	}

	parent := filepath.Join(tmpHome, "dev")
	if first.StackRoot != filepath.Join(parent, ".devstack-stacks", "navexa", "feat") {
		t.Errorf("navexa stack root = %q, want the workspace name in the path", first.StackRoot)
	}
	if second.StackRoot != filepath.Join(parent, ".devstack-stacks", "southfoundry", "feat") {
		t.Errorf("southfoundry stack root = %q, want the workspace name in the path", second.StackRoot)
	}
	for _, root := range []string{first.StackRoot, second.StackRoot} {
		if _, err := os.Stat(filepath.Join(root, "backend")); err != nil {
			t.Errorf("worktree missing under %s: %v", root, err)
		}
	}
}

// A record holds an absolute Root, so a stack created before the path carried
// the workspace name must keep working: devstack reads the record and never
// recomputes the path.
func TestAStackRecordedAtTheOldRootStillResolvesAndRemoves(t *testing.T) {
	base := newBase(t)
	noDaemon(t)

	created, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"backend"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}

	// Put the stack where the previous version of devstack put it: the parent
	// of the base checkout, with no workspace name in the path.
	oldRoot := filepath.Join(filepath.Dir(base.Path), ".devstack-stacks", "feat")
	if err := os.MkdirAll(oldRoot, 0755); err != nil {
		t.Fatal(err)
	}
	moved := map[string]string{}
	for _, s := range rec.Overlay {
		from := rec.Worktrees[s]
		to := filepath.Join(oldRoot, filepath.Base(from))
		git(t, filepath.Join(base.Path, s), "worktree", "move", from, to)
		moved[s] = to
	}
	manifest, err := os.ReadFile(config.WorkspaceManifestPath(created.StackRoot))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, config.WorkspaceManifestPath(oldRoot), strings.ReplaceAll(string(manifest), created.StackRoot, oldRoot))
	if err := os.RemoveAll(created.StackRoot); err != nil {
		t.Fatal(err)
	}
	rec.Root = oldRoot
	rec.Worktrees = moved
	if err := upsertStack(*rec); err != nil {
		t.Fatalf("upsertStack: %v", err)
	}

	old, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack on the old record: %v", err)
	}
	if old.Root != oldRoot {
		t.Fatalf("Root = %q, want the recorded old root %q", old.Root, oldRoot)
	}
	rw, err := ResolveWorktree(old)
	if err != nil {
		t.Fatalf("ResolveWorktree on the old record: %v", err)
	}
	if got := rw.Services["backend"].RepoPath; got != moved["backend"] {
		t.Errorf("backend RepoPath = %q, want the old worktree %q", got, moved["backend"])
	}
	if err := CheckRemovable(base, "feat", false); err != nil {
		t.Fatalf("CheckRemovable on the old record: %v", err)
	}
	rm, err := Remove(base, "feat", false)
	if err != nil {
		t.Fatalf("Remove on the old record: %v", err)
	}
	if len(rm.RemovedWorktrees) != 2 || !rm.RootRemoved {
		t.Errorf("Remove result = %+v, want both worktrees and the old root removed", rm)
	}
	if _, err := os.Stat(oldRoot); !os.IsNotExist(err) {
		t.Errorf("the old stack root %s was left behind", oldRoot)
	}
}

// newSplitBase gives the good repository a name that sorts before the bad one,
// so the failure always lands after a worktree is already on disk.
func newSplitBase(t *testing.T) *workspace.Workspace {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot := filepath.Join(tmpHome, "dev", "navexa")
	writeFile(t, filepath.Join(baseRoot, "aaa", "devstack.service.yaml"), svcManifest("aaa"))
	git(t, filepath.Join(baseRoot, "aaa"), "init", "-q", "-b", "main")
	git(t, filepath.Join(baseRoot, "aaa"), "add", "-f", ".")
	git(t, filepath.Join(baseRoot, "aaa"), "commit", "-q", "-m", "init")
	git(t, filepath.Join(baseRoot, "aaa"), "branch", "startpoint")

	makeRepo(t, filepath.Join(baseRoot, "zzz"), svcManifest("zzz"))

	writeFile(t, filepath.Join(baseRoot, "devstack.workspace.yaml"), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./aaa
      - ./zzz
calls:
  zzz:
    - aaa
`)
	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: baseRoot, TiltPort: 10350}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	return base
}

// A create that fails part-way used to leave worktrees the record knew nothing
// about, and every retry died on "already exists". stack rm could not help,
// because the record was written last and so did not exist.
func TestCreateUnwindsItsWorktreesWhenItFailsPartWay(t *testing.T) {
	base := newSplitBase(t)
	noDaemon(t)

	// startpoint exists in aaa and not in zzz, so aaa's worktree is built and
	// zzz then fails.
	_, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"aaa"}, From: "refs/heads/startpoint"})
	if err == nil {
		t.Fatal("Create with a ref that one repository does not have = nil, want an error")
	}

	stackRoot := filepath.Join(filepath.Dir(base.Path), ".devstack-stacks", "navexa", "feat")
	if _, statErr := os.Stat(stackRoot); !os.IsNotExist(statErr) {
		t.Errorf("the failed create left the stack root %s behind: %v", stackRoot, statErr)
	}
	if list := gitOut(t, filepath.Join(base.Path, "aaa"), "worktree", "list"); strings.Contains(list, "feat") {
		t.Errorf("the failed create left a worktree registered in git:\n%s", list)
	}
	if _, err := FindStack(base.Name, "feat"); err == nil {
		t.Error("the failed create recorded the stack")
	}

	// The retry must not die on "already exists".
	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"aaa"}})
	if err != nil {
		t.Fatalf("the retry after a failed create: %v", err)
	}
	if res.StackRoot != stackRoot {
		t.Errorf("StackRoot = %q, want %q", res.StackRoot, stackRoot)
	}
	for _, s := range []string{"aaa", "zzz"} {
		if _, err := os.Stat(filepath.Join(stackRoot, s)); err != nil {
			t.Errorf("the retry did not build the worktree for %s: %v", s, err)
		}
	}
}

// The same for add: the worktrees it built before the failure are removed, so
// the retry is clean. The worktrees the stack already had are untouched.
func TestAddUnwindsItsWorktreesWhenItFailsPartWay(t *testing.T) {
	base := newAddBase(t)
	created := newStack(t, base, "api")

	// startpoint exists in billing and not in worker.
	git(t, filepath.Join(base.Path, "billing"), "branch", "startpoint")
	apiHead := gitOut(t, worktreesByService(created)["api"].Path, "rev-parse", "HEAD")

	_, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"money"}, From: "refs/heads/startpoint"})
	if err == nil {
		t.Fatal("Add with a ref that one repository does not have = nil, want an error")
	}
	if _, statErr := os.Stat(filepath.Join(created.StackRoot, "billing")); !os.IsNotExist(statErr) {
		t.Errorf("the failed add left the billing worktree behind: %v", statErr)
	}
	if list := gitOut(t, filepath.Join(base.Path, "billing"), "worktree", "list"); strings.Contains(list, "feat") {
		t.Errorf("the failed add left a worktree registered in git:\n%s", list)
	}
	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if !reflect.DeepEqual(rec.Overlay, []string{"api", "web"}) {
		t.Errorf("record overlay = %v, want the stack as it was", rec.Overlay)
	}
	if got := gitOut(t, worktreesByService(created)["api"].Path, "rev-parse", "HEAD"); got != apiHead {
		t.Errorf("the failed add moved the api worktree to %s, want %s", got, apiHead)
	}

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"money"}})
	if err != nil {
		t.Fatalf("the retry after a failed add: %v", err)
	}
	if got := addedServices(res); !reflect.DeepEqual(got, []string{"billing", "worker"}) {
		t.Errorf("Added = %v, want [billing worker]", got)
	}
	for _, s := range []string{"billing", "worker"} {
		if _, err := os.Stat(filepath.Join(created.StackRoot, s)); err != nil {
			t.Errorf("the retry did not build the worktree for %s: %v", s, err)
		}
	}
}

// A caller is pulled into a stack on a detached HEAD, so nobody can commit in
// it. Naming it in stack add used to answer "nothing to add", which left stack
// rm plus stack create as the only route, and that destroys the stack's work.
func TestAddPromotesADetachedCallerOntoTheStackBranch(t *testing.T) {
	base := newAddBase(t)
	created := newStack(t, base, "api")

	web := worktreesByService(created)["web"].Path
	if got := gitOut(t, web, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Fatalf("web worktree branch = %q, want a detached HEAD to promote", got)
	}
	head := gitOut(t, web, "rev-parse", "HEAD")

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"web"}})
	if err != nil {
		t.Fatalf("Add of a detached caller: %v", err)
	}
	if !reflect.DeepEqual(res.Promoted, []string{"web"}) {
		t.Errorf("Promoted = %v, want [web]", res.Promoted)
	}
	if len(res.Added) != 0 {
		t.Errorf("Added = %+v, want none: a promotion is not an addition", res.Added)
	}
	if got := gitOut(t, web, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat" {
		t.Fatalf("web worktree branch = %q, want the stack's branch feat", got)
	}
	if got := gitOut(t, web, "rev-parse", "HEAD"); got != head {
		t.Errorf("promotion moved the worktree to %s, want the commit it had, %s", got, head)
	}

	// The branch is real, so the work can be committed and kept.
	writeFile(t, filepath.Join(web, "work.txt"), "committed in the stack\n")
	git(t, web, "add", "-f", ".")
	git(t, web, "commit", "-q", "-m", "work")
	if got := gitOut(t, filepath.Join(base.Path, "web"), "rev-parse", "refs/heads/feat"); got == head {
		t.Errorf("the commit did not advance branch feat in the source repository")
	}

	// A second call has nothing left to do.
	if _, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"web"}}); err == nil {
		t.Error("Add of a service already on the branch = nil, want the nothing-to-add error")
	}

	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if !reflect.DeepEqual(rec.Overlay, []string{"api", "web"}) {
		t.Errorf("record overlay = %v, want the stack unchanged", rec.Overlay)
	}
}

// One worktree, one branch: promoting a service promotes every service of its
// repository, and the report names them all.
func TestPromotionMovesEveryServiceOfTheRepository(t *testing.T) {
	base := newMonoBase(t)
	created := newStack(t, base, "backend")

	// Put both services of the repository into the stack as callers of nothing,
	// by adding one and then its sibling.
	if _, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"agent-rag"}}); err != nil {
		t.Fatalf("Add agent-rag: %v", err)
	}
	if _, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"agent-psql"}}); err != nil {
		t.Fatalf("Add agent-psql: %v", err)
	}

	mono := filepath.Join(created.StackRoot, "mono")
	git(t, mono, "checkout", "-q", "--detach")
	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"agent-rag"}})
	if err != nil {
		t.Fatalf("Add of a detached service: %v", err)
	}
	if !reflect.DeepEqual(res.Promoted, []string{"agent-psql", "agent-rag"}) {
		t.Errorf("Promoted = %v, want both services of the repository", res.Promoted)
	}
	if got := gitOut(t, mono, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat" {
		t.Errorf("repository worktree branch = %q, want feat", got)
	}
}
