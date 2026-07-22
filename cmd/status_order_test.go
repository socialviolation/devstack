package cmd

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/tilt"
)

func roiGroup() ([]string, map[string][]string) {
	members := []string{"navexa-agent", "navexa-mcp", "qdrant", "roi-datastores", "rutter-local"}
	deps := map[string][]string{
		"navexa-agent": {"navexa-mcp", "rutter-local", "roi-datastores"},
		"rutter-local": {"qdrant"},
	}
	return members, deps
}

func TestOrderGroupServicesDependenciesFirst(t *testing.T) {
	members, deps := roiGroup()
	got := orderGroupServices(members, deps)

	pos := map[string]int{}
	for i, svc := range got {
		pos[svc] = i
	}
	for svc, list := range deps {
		for _, dep := range list {
			if pos[dep] >= pos[svc] {
				t.Fatalf("dependency %q must precede %q, got order %v", dep, svc, got)
			}
		}
	}
}

func TestOrderGroupServicesEveryMemberExactlyOnce(t *testing.T) {
	members, deps := roiGroup()
	got := orderGroupServices(members, deps)

	if len(got) != len(members) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(members), got)
	}
	seen := append([]string(nil), got...)
	sort.Strings(seen)
	want := append([]string(nil), members...)
	sort.Strings(want)
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("got members %v, want %v", seen, want)
	}
}

func TestOrderGroupServicesDeterministic(t *testing.T) {
	members, deps := roiGroup()
	first := orderGroupServices(members, deps)
	for i := 0; i < 20; i++ {
		if got := orderGroupServices(members, deps); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d gave %v, want %v", i, got, first)
		}
	}

	want := []string{"navexa-mcp", "qdrant", "roi-datastores", "rutter-local", "navexa-agent"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("got %v, want alphabetical-tiebreak order %v", first, want)
	}
}

func TestOrderGroupServicesCycleReturnsAllMembers(t *testing.T) {
	members := []string{"a", "b", "c", "d"}
	deps := map[string][]string{
		"a": {"b"},
		"b": {"c"},
		"c": {"a"},
		"d": {"d"},
	}

	done := make(chan []string, 1)
	go func() { done <- orderGroupServices(members, deps) }()

	var got []string
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("orderGroupServices hung on a cyclic graph")
	}

	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(sorted, members) {
		t.Fatalf("got %v, want every member exactly once %v", got, members)
	}
}

func TestExtractPortsDedupes(t *testing.T) {
	links := []tilt.EndpointLink{
		{URL: "http://localhost:5433"},
		{URL: "http://localhost:6380"},
		{URL: "http://localhost:6380/health"},
		{URL: "http://localhost:5433"},
	}
	if got, want := extractPorts(links), ":5433 :6380"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExtractPortsPreservesFirstSeenOrder(t *testing.T) {
	links := []tilt.EndpointLink{
		{URL: "http://localhost:6333"},
		{URL: "http://localhost:6334"},
		{URL: "http://localhost:6333"},
	}
	got := extractPorts(links)
	if got != ":6333 :6334" {
		t.Fatalf("got %q, want %q", got, ":6333 :6334")
	}
	if strings.Count(got, ":6333") != 1 {
		t.Fatalf("port :6333 repeated in %q", got)
	}
}
