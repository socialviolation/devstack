package stack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

const apiSvc = `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
ports:
  http: 8080
`

const webSvc = `version: 1
service:
  name: web
runtime:
  run:
    command: npm run dev
ports:
  http: 3000
`

const billingSvc = `version: 1
service:
  name: billing
runtime:
  run:
    command: go run .
ports:
  http: 8090
`

const workerSvc = `version: 1
service:
  name: worker
runtime:
  run:
    command: go run .
`

// newAddBase is a base with a service outside the overlay a stack starts with,
// so there is something left to add: web calls api, and billing and worker are
// reachable from neither.
func newAddBase(t *testing.T) *workspace.Workspace {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	baseRoot := filepath.Join(tmpHome, "dev", "acme")
	makeRepo(t, filepath.Join(baseRoot, "api"), apiSvc)
	makeRepo(t, filepath.Join(baseRoot, "web"), webSvc)
	makeRepo(t, filepath.Join(baseRoot, "billing"), billingSvc)
	makeRepo(t, filepath.Join(baseRoot, "worker"), workerSvc)

	writeFile(t, filepath.Join(baseRoot, "devstack.workspace.yaml"), `version: 1
workspace:
  name: acme
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
      - ./web
      - ./billing
      - ./worker
groups:
  money:
    - billing
    - worker
calls:
  web:
    - api
`)

	if err := workspace.Register(workspace.Workspace{Name: "acme", Path: baseRoot, TiltPort: 10350}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	base, err := workspace.FindByName("acme")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	return base
}

func addedServices(res *AddResult) []string {
	out := make([]string, 0, len(res.Added))
	for _, m := range res.Added {
		out = append(out, m.Service)
	}
	return out
}

func newStack(t *testing.T, base *workspace.Workspace, repos ...string) *CreateResult {
	t.Helper()
	noDaemon(t)
	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: repos})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return res
}

// The worktrees a stack already has hold the work. Adding a service must build
// its worktree and touch nothing else.
func TestAddBuildsOnlyTheNewWorktree(t *testing.T) {
	base := newAddBase(t)
	created := newStack(t, base, "api")

	wt := worktreesByService(created)
	writeFile(t, filepath.Join(wt["api"].Path, "wip.txt"), "in progress\n")
	git(t, wt["api"].Path, "add", "-f", ".")
	git(t, wt["api"].Path, "commit", "-q", "-m", "wip")
	apiHead := gitOut(t, wt["api"].Path, "rev-parse", "HEAD")
	webHead := gitOut(t, wt["web"].Path, "rev-parse", "HEAD")

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"billing"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := addedServices(res); !reflect.DeepEqual(got, []string{"billing"}) {
		t.Fatalf("Added = %v, want [billing]", got)
	}
	if len(res.Worktrees) != 1 || res.Worktrees[0].Service != "billing" {
		t.Fatalf("Worktrees = %+v, want only billing", res.Worktrees)
	}
	billing := res.Worktrees[0].Path
	if got := gitOut(t, billing, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat" {
		t.Errorf("billing worktree branch = %q, want the stack's branch feat", got)
	}

	if got := gitOut(t, wt["api"].Path, "rev-parse", "HEAD"); got != apiHead {
		t.Errorf("api worktree HEAD moved to %s, want %s left as it was", got, apiHead)
	}
	if _, err := os.Stat(filepath.Join(wt["api"].Path, "wip.txt")); err != nil {
		t.Errorf("work in the existing worktree was lost: %v", err)
	}
	if got := gitOut(t, wt["web"].Path, "rev-parse", "HEAD"); got != webHead {
		t.Errorf("web worktree HEAD moved to %s, want %s left as it was", got, webHead)
	}

	if !reflect.DeepEqual(res.Overlay, []string{"api", "billing", "web"}) {
		t.Errorf("Overlay = %v, want [api billing web]", res.Overlay)
	}
	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if !reflect.DeepEqual(rec.Overlay, []string{"api", "billing", "web"}) {
		t.Errorf("record overlay = %v, want [api billing web]", rec.Overlay)
	}
	if rec.Worktrees["billing"] != billing {
		t.Errorf("record worktree for billing = %q, want %q", rec.Worktrees["billing"], billing)
	}

	manifest, err := os.ReadFile(res.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(manifest), billing) {
		t.Errorf("manifest does not list the added worktree %s:\n%s", billing, manifest)
	}
	if !strings.Contains(string(manifest), wt["api"].Path) {
		t.Errorf("manifest dropped the worktree the stack already had:\n%s", manifest)
	}
}

// The trap: the stack's copies may be serving on these ports right now, so an
// addition that re-allocates the whole key set moves every one of them.
func TestAddLeavesExistingPortsWhereTheyAre(t *testing.T) {
	base := newAddBase(t)
	created := newStack(t, base, "api")

	before := map[string]int{}
	for k, v := range created.Ports {
		before[k] = v
	}
	if before[QualifyPortKey("api", "http")] == 0 {
		t.Fatalf("no port allocated for api/http: %v", before)
	}

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"billing"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	kept := map[string]int{}
	for k := range before {
		kept[k] = rec.Ports[k]
	}
	if !reflect.DeepEqual(kept, before) {
		t.Errorf("ports moved: %v, want %v unchanged", kept, before)
	}

	ledger, err := workspace.LoadPorts(rec.RuntimeKey())
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	if !reflect.DeepEqual(ledger, rec.Ports) {
		t.Errorf("ledger %v and record %v disagree", ledger, rec.Ports)
	}
	billingPort := ledger[QualifyPortKey("billing", "http")]
	if billingPort == 0 {
		t.Fatalf("no port allocated for the added service: %v", ledger)
	}
	if billingPort == before[QualifyPortKey("api", "http")] {
		t.Errorf("billing was handed api's port %d", billingPort)
	}
	if !reflect.DeepEqual(res.Ports, map[string]int{QualifyPortKey("billing", "http"): billingPort}) {
		t.Errorf("result ports = %v, want only the newly allocated one", res.Ports)
	}
}

func TestAddExpandsAndRecordsAGroup(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"money"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := addedServices(res); !reflect.DeepEqual(got, []string{"billing", "worker"}) {
		t.Fatalf("Added = %v, want the group's members [billing worker]", got)
	}
	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if !reflect.DeepEqual(rec.Groups, []string{"money"}) {
		t.Errorf("record groups = %v, want [money]", rec.Groups)
	}
	if !reflect.DeepEqual(rec.Overlay, []string{"api", "billing", "web", "worker"}) {
		t.Errorf("record overlay = %v, want [api billing web worker]", rec.Overlay)
	}
	if _, ok := rec.Ports[QualifyPortKey("worker", "http")]; ok {
		t.Errorf("worker declares no ports and must not be allocated one: %v", rec.Ports)
	}
}

func TestAddReportsAServiceAlreadyInTheStack(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"api", "billing"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !reflect.DeepEqual(res.AlreadyPresent, []string{"api"}) {
		t.Errorf("AlreadyPresent = %v, want [api]", res.AlreadyPresent)
	}
	if got := addedServices(res); !reflect.DeepEqual(got, []string{"billing"}) {
		t.Errorf("Added = %v, want [billing]", got)
	}
	if len(res.Worktrees) != 1 {
		t.Errorf("built %d worktrees, want only the added service's", len(res.Worktrees))
	}

	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if !reflect.DeepEqual(rec.Overlay, []string{"api", "billing", "web"}) {
		t.Errorf("record overlay = %v, want api listed once", rec.Overlay)
	}
}

func TestAddNothingNewIsAnError(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	// api is on the stack's branch already. web is in the stack too, but on a
	// detached HEAD, so naming it is a promotion and not nothing.
	_, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"api"}})
	if err == nil {
		t.Fatal("Add of services the stack already has = nil, want an error")
	}
	if !strings.Contains(err.Error(), "nothing to add") {
		t.Errorf("error should say there is nothing to add: %v", err)
	}
}

// The stack's branch may already exist in a repo the stack never included —
// picking it up must keep its commits, not cut a new branch over them.
func TestAddAttachesAnExistingBranchInsteadOfRecutting(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	billing := filepath.Join(base.Path, "billing")
	writeFile(t, filepath.Join(billing, "earlier.txt"), "yesterday\n")
	git(t, billing, "checkout", "-q", "-b", "feat")
	git(t, billing, "add", "-f", ".")
	git(t, billing, "commit", "-q", "-m", "earlier work")
	featTip := gitOut(t, billing, "rev-parse", "HEAD")
	git(t, billing, "checkout", "-q", "main")

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"billing"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	path := res.Worktrees[0].Path
	if got := gitOut(t, path, "rev-parse", "HEAD"); got != featTip {
		t.Errorf("existing branch re-cut at %s, want its own tip %s", got, featTip)
	}
	if _, err := os.Stat(filepath.Join(path, "earlier.txt")); err != nil {
		t.Errorf("attaching the existing branch lost its commits: %v", err)
	}
}

func TestAddFromOverridesTheDefaultBranch(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	billing := filepath.Join(base.Path, "billing")
	parkedTip := park(t, billing)
	git(t, billing, "checkout", "-q", "main")

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"billing"}, From: "refs/heads/parked"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	path := res.Worktrees[0].Path
	if got := gitOut(t, path, "rev-parse", "HEAD"); got != parkedTip {
		t.Errorf("added worktree at %s, want the --from ref %s", got, parkedTip)
	}
	if _, err := os.Stat(filepath.Join(path, "parked.txt")); err != nil {
		t.Errorf("--from ref's commit missing from the worktree: %v", err)
	}
}

// A service is added because something the caller named needs it, so the
// services that call it come too — the same overlay rule create applies.
func TestAddPullsInCallersOfTheAddedService(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "billing")

	res, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"api"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	reason := map[string]string{}
	for _, m := range res.Added {
		reason[m.Service] = m.Reason
	}
	if reason["api"] != "changed" {
		t.Errorf("api reason = %q, want changed", reason["api"])
	}
	if reason["web"] != "caller" {
		t.Errorf("web reason = %q, want caller (it calls api)", reason["web"])
	}
	for _, wt := range res.Worktrees {
		if wt.Service == "web" && !wt.Detached {
			t.Errorf("a service pulled in as a caller must be detached, got %+v", wt)
		}
		if wt.Service == "api" && wt.Branch != "feat" {
			t.Errorf("a named service must get the stack's branch, got %+v", wt)
		}
	}
}

func TestAddUnknownName(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	if _, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"nope"}}); err == nil {
		t.Fatal("Add of an unknown name = nil, want an error")
	}
	if _, err := Add(AddInput{Base: base, Name: "ghost", Members: []string{"billing"}}); err == nil {
		t.Fatal("Add to an unknown stack = nil, want an error")
	}
}
