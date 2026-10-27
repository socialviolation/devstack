package stack

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func TestDropRemovesTheServiceFromRecordManifestAndPorts(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")
	if _, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"billing"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	before, _ := FindStack(base.Name, "feat")
	apiPort := before.Ports["api/http"]
	billingPath := before.Worktrees["billing"]

	res, err := Drop(DropInput{Base: base, Name: "feat", Services: []string{"billing"}})
	if err != nil {
		t.Fatalf("Drop: %v", err)
	}

	rec, _ := FindStack(base.Name, "feat")
	if !reflect.DeepEqual(rec.Overlay, []string{"api", "web"}) {
		t.Errorf("Overlay = %v, want [api web]", rec.Overlay)
	}
	if _, ok := rec.Worktrees["billing"]; ok {
		t.Errorf("Worktrees still name billing: %v", rec.Worktrees)
	}
	if _, ok := rec.Ports["billing/http"]; ok {
		t.Errorf("record Ports still hold billing/http: %v", rec.Ports)
	}
	ports, err := workspace.LoadPorts(rec.RuntimeKey())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ports["billing/http"]; ok {
		t.Errorf("ports.json still holds billing/http: %v", ports)
	}
	if ports["api/http"] != apiPort {
		t.Errorf("api/http = %d, want it unchanged at %d", ports["api/http"], apiPort)
	}
	if !reflect.DeepEqual(res.ReleasedPorts, []string{"billing/http"}) {
		t.Errorf("ReleasedPorts = %v, want [billing/http]", res.ReleasedPorts)
	}

	rw, err := config.ResolveWorkspace(rec.Root)
	if err != nil {
		t.Fatalf("resolve the stack manifest: %v", err)
	}
	if _, ok := rw.Services["billing"]; ok {
		t.Errorf("the stack manifest still lists billing")
	}
	for _, repo := range rw.Manifest.Workspace.RepoDiscovery.Repos {
		if repo == billingPath {
			t.Errorf("the stack manifest still lists the billing worktree %s", repo)
		}
	}

	if _, err := os.Stat(billingPath); err != nil {
		t.Errorf("the billing worktree is gone from disk: %v", err)
	}
	if len(res.Kept) != 1 || !strings.Contains(res.Kept[0].RemoveCommand, "worktree remove") {
		t.Errorf("Kept = %+v, want the billing worktree and its remove command", res.Kept)
	}
}

func TestDropRefusesAServiceAnotherStillDependsOn(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	_, err := Drop(DropInput{Base: base, Name: "feat", Services: []string{"api"}})
	if err == nil || !strings.Contains(err.Error(), "web depends on api") {
		t.Fatalf("Drop() error = %v, want a refusal naming web", err)
	}
	rec, _ := FindStack(base.Name, "feat")
	if !reflect.DeepEqual(rec.Overlay, []string{"api", "web"}) {
		t.Errorf("Overlay = %v, want it untouched", rec.Overlay)
	}
}

func TestDropRefusesAServiceTheStackDoesNotRun(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	_, err := Drop(DropInput{Base: base, Name: "feat", Services: []string{"billing"}})
	if err == nil || !strings.Contains(err.Error(), `does not run "billing"`) {
		t.Fatalf("Drop() error = %v, want a refusal", err)
	}
}

func TestDropRunsTheTeardownForTheRemovedServicesBeforeReleasingTheirPorts(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")
	if _, err := Add(AddInput{Base: base, Name: "feat", Members: []string{"billing"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var got []string
	var recorded, allocated bool
	_, err := Drop(DropInput{Base: base, Name: "feat", Services: []string{"billing"}, Teardown: func(removed []string) {
		got = removed
		rec, _ := FindStack(base.Name, "feat")
		_, recorded = rec.Ports["billing/http"]
		ports, _ := workspace.LoadPorts(rec.RuntimeKey())
		_, allocated = ports["billing/http"]
	}})
	if err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"billing"}) {
		t.Errorf("Teardown got %v, want [billing]", got)
	}
	if !recorded || !allocated {
		t.Errorf("at teardown billing/http recorded=%v allocated=%v, want both still held", recorded, allocated)
	}
}

func TestDropRunsNoTeardownForARefusal(t *testing.T) {
	base := newAddBase(t)
	newStack(t, base, "api")

	ran := false
	_, err := Drop(DropInput{Base: base, Name: "feat", Services: []string{"api"}, Teardown: func([]string) { ran = true }})
	if err == nil {
		t.Fatal("Drop() = nil, want a refusal")
	}
	if ran {
		t.Error("the teardown ran for a removal that refused")
	}
}
