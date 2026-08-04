package stack

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func topoWithGroups() *config.TopologyGraph {
	return &config.TopologyGraph{
		Services: map[string]*config.ServiceTopology{
			"api": {}, "frontend": {}, "orbit": {}, "importer": {},
		},
		Groups: map[string][]string{
			"core":    {"api", "frontend", "orbit"},
			"imports": {"importer"},
			"empty":   {},
		},
	}
}

func TestExpandGroupsTurnsAGroupIntoItsMembers(t *testing.T) {
	services, groups, err := expandGroups(topoWithGroups(), "navexa", []string{"core"})
	if err != nil {
		t.Fatalf("expandGroups: %v", err)
	}
	if got := strings.Join(services, ","); got != "api,frontend,orbit" {
		t.Errorf("services = %q, want the group's members", got)
	}
	if got := strings.Join(groups, ","); got != "core" {
		t.Errorf("groups = %q, want core recorded as covered", got)
	}
}

func TestExpandGroupsMixesServicesAndGroupsWithoutDuplicating(t *testing.T) {
	services, groups, err := expandGroups(topoWithGroups(), "navexa", []string{"api", "core", "importer"})
	if err != nil {
		t.Fatalf("expandGroups: %v", err)
	}
	if got := strings.Join(services, ","); got != "api,frontend,orbit,importer" {
		t.Errorf("services = %q, want api once, then the rest of core, then importer", got)
	}
	if got := strings.Join(groups, ","); got != "core" {
		t.Errorf("groups = %q, want only the named group", got)
	}
}

// --repos takes the services a stack changes, so a name that is both is read as
// the service. Expanding it as a group instead would silently pull in services
// the caller did not name.
func TestExpandGroupsPrefersTheServiceWhenANameIsBoth(t *testing.T) {
	topo := topoWithGroups()
	topo.Groups["api"] = []string{"api", "frontend"}

	services, groups, err := expandGroups(topo, "navexa", []string{"api"})
	if err != nil {
		t.Fatalf("expandGroups: %v", err)
	}
	if got := strings.Join(services, ","); got != "api" {
		t.Errorf("services = %q, want just the service", got)
	}
	if len(groups) != 0 {
		t.Errorf("groups = %v, want none — it was read as a service", groups)
	}
}

func TestExpandGroupsUnknownNameListsBoth(t *testing.T) {
	_, _, err := expandGroups(topoWithGroups(), "navexa", []string{"nope"})
	if err == nil {
		t.Fatal("expandGroups accepted a name that is neither a service nor a group")
	}
	for _, want := range []string{"services:", "groups:", "core", "api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name what is available (%q missing): %v", want, err)
		}
	}
}

func TestExpandGroupsEmptyGroup(t *testing.T) {
	if _, _, err := expandGroups(topoWithGroups(), "navexa", []string{"empty"}); err == nil {
		t.Fatal("expandGroups accepted a group with no services")
	}
}

// A stack overlays what it changes plus the callers of that, so naming a group
// does not guarantee the group came with it. The count alone reads as success;
// the shortfall has to name where the rest actually run.
func TestCoverageNamesWhatStaysOnBase(t *testing.T) {
	base := map[string][]string{"core": {"api", "frontend", "orbit"}}

	cov := CoverageOf([]string{"core"}, []string{"api"}, base)
	if len(cov) != 1 {
		t.Fatalf("CoverageOf = %v, want one group", cov)
	}
	if cov[0].Complete() {
		t.Error("Complete() = true with two members missing")
	}
	if got := cov[0].Label(); got != "core 1/3" {
		t.Errorf("Label() = %q, want core 1/3", got)
	}
	got := cov[0].Sentence()
	for _, want := range []string{"1/3", "frontend", "orbit", "base"} {
		if !strings.Contains(got, want) {
			t.Errorf("Sentence() = %q, want it to mention %q", got, want)
		}
	}
}

func TestCoverageCompleteWhenTheWholeGroupIsInTheStack(t *testing.T) {
	base := map[string][]string{"core": {"api", "frontend"}}

	cov := CoverageOf([]string{"core"}, []string{"api", "frontend", "extra"}, base)
	if !cov[0].Complete() {
		t.Errorf("Complete() = false for %v, want true", cov[0])
	}
	if got := cov[0].Sentence(); strings.Contains(got, "base") {
		t.Errorf("Sentence() = %q, want no shortfall clause when nothing is missing", got)
	}
}

func TestCoverageIgnoresGroupsBaseNoLongerHas(t *testing.T) {
	if cov := CoverageOf([]string{"gone"}, []string{"api"}, map[string][]string{}); len(cov) != 0 {
		t.Errorf("CoverageOf = %v, want nothing for a group base no longer declares", cov)
	}
}
