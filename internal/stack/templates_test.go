package stack

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func newTemplateBase(t *testing.T) *workspace.Workspace {
	t.Helper()
	base := newSharedBase(t)
	writeFile(t, filepath.Join(base.Path, "devstack.workspace.yaml"), `version: 1
workspace:
  name: shop
  repoDiscovery:
    mode: explicit
    repos: [./db, ./api, ./web, ./billing, ./reports]
shared: [db]
groups:
  front: [web, db]
environments:
  prod-rag:
    values:
      RAG: prod
templates:
  agent:
    purpose: Agent work.
    services: [front, db]
    env: prod-rag
`)
	return base
}

func TestCreateFromTemplateSetsMembersEnvAndTemplate(t *testing.T) {
	base := newTemplateBase(t)
	noDaemon(t)

	res, err := Create(CreateInput{Base: base, Name: "feat", Template: "agent"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := memberNames(res); !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Errorf("members = %v, want the front group, its needs, and no shared db", got)
	}
	if res.Template != "agent" || res.Env != "prod-rag" {
		t.Errorf("result template = %q env = %q, want agent and prod-rag", res.Template, res.Env)
	}
	rec, err := FindStack("shop", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Template != "agent" || rec.Env != "prod-rag" {
		t.Errorf("record template = %q env = %q, want agent and prod-rag", rec.Template, rec.Env)
	}
	if !reflect.DeepEqual(rec.Groups, []string{"front"}) {
		t.Errorf("record groups = %v, want [front]", rec.Groups)
	}
	infos, err := List("shop")
	if err != nil || len(infos) != 1 || infos[0].Template != "agent" {
		t.Errorf("List = %+v, %v, want the template on the stack", infos, err)
	}
}

func TestCreateFromTemplateMergesExtraRepos(t *testing.T) {
	base := newTemplateBase(t)
	noDaemon(t)

	res, err := Create(CreateInput{Base: base, Name: "feat", Template: "agent", Repos: []string{"billing"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := memberNames(res); !reflect.DeepEqual(got, []string{"api", "billing", "web"}) {
		t.Errorf("members = %v, want the template members plus billing", got)
	}
}

func TestCreateWithoutTemplateSetsNoEnv(t *testing.T) {
	base := newTemplateBase(t)
	noDaemon(t)

	if _, err := Create(CreateInput{Base: base, Name: "feat", Repos: []string{"web"}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec, err := FindStack("shop", "feat")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Template != "" || rec.Env != "" {
		t.Errorf("record template = %q env = %q, want both empty", rec.Template, rec.Env)
	}
}

func TestCreateRefusesAnUnknownTemplateAndListsTheTemplates(t *testing.T) {
	base := newTemplateBase(t)
	noDaemon(t)

	_, err := Create(CreateInput{Base: base, Name: "feat", Template: "ghost"})
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) || !strings.Contains(err.Error(), "Templates: agent") {
		t.Fatalf("Create = %v, want a refusal that names ghost and lists agent", err)
	}
	if _, ferr := FindStack("shop", "feat"); ferr == nil {
		t.Error("a refused create must record no stack")
	}
}
