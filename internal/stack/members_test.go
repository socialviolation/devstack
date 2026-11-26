package stack

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func newSharedBase(t *testing.T) *workspace.Workspace {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	root := filepath.Join(tmpHome, "dev", "shop")
	svc := func(name, extra string) string {
		return "version: 1\nservice:\n  name: " + name + "\nruntime:\n  run:\n    command: go run .\nports:\n  http: 8080\n" + extra
	}
	makeRepo(t, filepath.Join(root, "db"), svc("db", ""))
	makeRepo(t, filepath.Join(root, "api"), svc("api", "needs: [db]\n"))
	makeRepo(t, filepath.Join(root, "web"), svc("web", "needs: [api]\ncalls: [billing]\nenv:\n  values:\n    REPORTS: \"${reports.url}\"\n"))
	makeRepo(t, filepath.Join(root, "billing"), svc("billing", ""))
	makeRepo(t, filepath.Join(root, "reports"), svc("reports", ""))
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"), `version: 1
workspace:
  name: shop
  repoDiscovery:
    mode: explicit
    repos: [./db, ./api, ./web, ./billing, ./reports]
shared: [db]
groups:
  front: [web, db]
`)
	if err := workspace.Register(workspace.Workspace{Name: "shop", Path: root, TiltPort: 10350}); err != nil {
		t.Fatal(err)
	}
	base, err := workspace.FindByName("shop")
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func memberNames(res *CreateResult) []string {
	var out []string
	for _, m := range res.Members {
		out = append(out, m.Service)
	}
	return out
}

func TestCreateRunsTheNeedsClosureWithoutSharedServicesOrCallers(t *testing.T) {
	base := newSharedBase(t)
	noDaemon(t)

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"web"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := memberNames(res); !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Errorf("members = %v, want web and the api it needs, and not the shared db", got)
	}
	for _, m := range res.Members {
		if m.Service == "api" && m.Describe() != "needed: web needs api" {
			t.Errorf("api = %q, want the needs chain", m.Describe())
		}
	}

	api, err := Create(CreateInput{Base: base, Name: "api-only", Repos: []string{"api"}})
	if err != nil {
		t.Fatalf("Create api: %v", err)
	}
	if got := memberNames(api); !reflect.DeepEqual(got, []string{"api"}) {
		t.Errorf("members = %v, want only api: web calls it, and a caller never comes in", got)
	}
}

func TestCreateWarnsAboutAServiceAMemberUsesAndDoesNotFail(t *testing.T) {
	base := newSharedBase(t)
	noDaemon(t)

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"web"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	joined := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"billing is not in this stack. Run `stack add billing` if the change needs it.",
		"reports is not in this stack. Run `stack add reports` if the change needs it.",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings must contain %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "db is not in this stack") {
		t.Errorf("a shared service is never outside the stack:\n%s", joined)
	}

	rec, err := FindStack(base.Name, "feat")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := GenerateOptions(rec, rec.Members)
	if err != nil {
		t.Fatalf("GenerateOptions: %v", err)
	}
	if got := opts.Book["reports"]["http"]; got != 8080 {
		t.Errorf("a ref to a non-member resolves to %d, want its declared port 8080", got)
	}
	if !opts.Shared["db"] {
		t.Errorf("Shared = %v, want db", opts.Shared)
	}
}

func TestCreateAndAddRefuseASharedService(t *testing.T) {
	base := newSharedBase(t)
	noDaemon(t)

	_, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"db"}})
	if err == nil || !strings.Contains(err.Error(), "shared service") || !strings.Contains(err.Error(), "The shared set is: db") {
		t.Fatalf("Create(db) = %v, want a refusal that names the shared set", err)
	}
	if _, err := FindStack(base.Name, "feat"); err == nil {
		t.Error("a refused create recorded the stack")
	}

	if _, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"billing"}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = Add(AddInput{Base: base, Name: "feat", Members: []string{"db"}})
	if err == nil || !strings.Contains(err.Error(), "The shared set is: db") {
		t.Fatalf("Add(db) = %v, want a refusal that names the shared set", err)
	}
}

func TestCreateSkipsTheSharedMembersOfAGroup(t *testing.T) {
	base := newSharedBase(t)
	noDaemon(t)

	res, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"front"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := memberNames(res); !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Errorf("members = %v, want the group without its shared db", got)
	}
}

func TestCreateWithNoBranchDetachesEveryWorktree(t *testing.T) {
	base := newSharedBase(t)
	noDaemon(t)
	tip := gitOut(t, filepath.Join(base.Path, "web"), "rev-parse", "HEAD")

	res, err := Create(CreateInput{Base: base, Name: "main", Repos: []string{"web"}, Detach: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, wt := range res.Worktrees {
		if !wt.Detached || wt.Branch != "" {
			t.Errorf("worktree %s = %+v, want detached", wt.Service, wt)
		}
		if got := gitOut(t, wt.RepoPath, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
			t.Errorf("worktree %s is on branch %q, want a detached HEAD", wt.Service, got)
		}
	}
	if got := gitOut(t, worktreesByService(res)["web"].Path, "rev-parse", "HEAD"); got != tip {
		t.Errorf("web worktree at %s, want the default branch tip %s", got, tip)
	}
	if branches := gitOut(t, filepath.Join(base.Path, "web"), "branch", "--list", "main"); !strings.Contains(branches, "main") {
		t.Errorf("the default branch main is gone: %q", branches)
	}
	rec, err := FindStack(base.Name, "main")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Branch != "" {
		t.Errorf("record branch = %q, want none", rec.Branch)
	}

	if _, err := Create(CreateInput{Base: base, Name: "both", Repos: []string{"web"}, Detach: true, Branch: "x"}); err == nil {
		t.Error("Create with a branch and detach = nil, want a refusal")
	}
}

func TestRecordReadsTheOldOverlayKeyAsMembers(t *testing.T) {
	var rec Record
	if err := json.Unmarshal([]byte(`{"name":"feat","base":"shop","overlay":["api","web"]}`), &rec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.Members, []string{"api", "web"}) {
		t.Errorf("Members = %v, want the old overlay list", rec.Members)
	}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"members":["api","web"]`) || strings.Contains(string(out), `"overlay"`) {
		t.Errorf("a record writes members, never overlay: %s", out)
	}

	var both Record
	if err := json.Unmarshal([]byte(`{"name":"feat","members":["api"],"overlay":["web"]}`), &both); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(both.Members, []string{"api"}) {
		t.Errorf("Members = %v, want the new key to win", both.Members)
	}
}

func TestRequireSharedNamesTheStackFlag(t *testing.T) {
	base := newSharedBase(t)
	rw, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireShared(rw, []string{"db"}, "--stack <name>"); err != nil {
		t.Errorf("RequireShared(db) = %v, want nil for a shared service", err)
	}
	err = RequireShared(rw, []string{"api"}, "--stack <name>")
	if err == nil || !strings.Contains(err.Error(), "--stack <name>") {
		t.Errorf("RequireShared(api) = %v, want an error that names --stack", err)
	}
}
