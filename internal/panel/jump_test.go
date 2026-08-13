package panel

import "testing"

func linkSnapshot() Snapshot {
	return Snapshot{
		Infra: []Service{
			{Name: "otel", State: "running", URLs: []string{"http://localhost:5080"}, Infra: true},
		},
		Workspaces: []Workspace{{
			Name: "navexa",
			Base: []Service{
				{Name: "orbit-web", State: "running", URLs: []string{"https://box.ts.net:9420"}},
				{Name: "navexa-api", State: "running"},
			},
			Stacks: []Stack{{Name: "offers", Up: true, Services: []Service{
				{Name: "orbit-web", State: "stopped", URLs: []string{"https://box.ts.net:8411"}},
			}}},
		}}}
}

// The picker exists to open an address. A row with none is noise in it.
func TestOnlyTheRowsWithAnAddressReachThePicker(t *testing.T) {
	links := collectLinks(linkSnapshot())

	if len(links) != 3 {
		t.Fatalf("collectLinks gave %d links, want 3", len(links))
	}
	for _, l := range links {
		if l.URL == "" {
			t.Errorf("link %q has no address", l.Label)
		}
		if l.Label == "navexa-api" {
			t.Error("a service with no address reached the picker")
		}
	}
}

// A stopped copy still has an address, and it still belongs in the list. The
// running copies come first, because that is the one a reader means.
func TestARunningCopyComesFirst(t *testing.T) {
	links := collectLinks(linkSnapshot())

	if links[len(links)-1].State != "stopped" {
		t.Errorf("the stopped copy is at %d, want it last", len(links)-1)
	}
}

// Typing the start of a service name has to find that service, and the copy
// that runs has to come before the copy that does not.
func TestTypingAServiceNameFindsItsAddresses(t *testing.T) {
	links := filterLinks(collectLinks(linkSnapshot()), "orbit")

	if len(links) != 2 {
		t.Fatalf("filterLinks gave %d links, want the two orbit-web copies", len(links))
	}
	if links[0].Group != "base" {
		t.Errorf("first hit is the %s copy, want base: it is the one running", links[0].Group)
	}
}

// A reader who remembers the stack, or the port, and nothing else still finds
// the row: the query matches the whole line, address included.
func TestAQueryMatchesTheStackAndTheAddress(t *testing.T) {
	links := collectLinks(linkSnapshot())

	if got := filterLinks(links, "offers"); len(got) != 1 || got[0].Group != "offers" {
		t.Errorf("query %q gave %v, want the stack's copy", "offers", got)
	}
	if got := filterLinks(links, "8411"); len(got) != 1 || got[0].URL != "https://box.ts.net:8411" {
		t.Errorf("query %q gave %v, want the copy on that port", "8411", got)
	}
}

// Letters that follow each other beat letters scattered over the line, or the
// first hit is never the row the reader typed for.
func TestALetterRunBeatsAScatteredMatch(t *testing.T) {
	run, ok := fuzzyScore("orbit-web base", "web")
	if !ok {
		t.Fatal("the run did not match")
	}
	scattered, ok := fuzzyScore("workspace element boxed", "web")
	if !ok {
		t.Fatal("the scattered letters did not match")
	}
	if run <= scattered {
		t.Errorf("run scored %d, scattered %d: the run must win", run, scattered)
	}
}

func TestAQueryThatIsNotThereMatchesNothing(t *testing.T) {
	if _, ok := fuzzyScore("orbit-web base", "zzz"); ok {
		t.Error("a query that is not in the line matched it")
	}
}

func TestAnEmptyQueryKeepsEveryLink(t *testing.T) {
	links := collectLinks(linkSnapshot())
	if got := filterLinks(links, "   "); len(got) != len(links) {
		t.Errorf("an empty query gave %d links, want all %d", len(got), len(links))
	}
}

// A local address opens on this machine and nowhere else. The tailnet address
// is the one to give somebody, so the picker offers it first.
func TestATailnetAddressComesBeforeALocalOne(t *testing.T) {
	snap := Snapshot{
		Infra: []Service{{
			Name: "daemon", State: "running", Infra: true,
			URLs: []string{"http://localhost:10300"},
		}},
		Workspaces: []Workspace{{
			Name: "navexa",
			Base: []Service{{
				Name: "orbit-web", State: "running",
				URLs: []string{"https://box.ts.net:9420"},
			}},
		}}}

	links := collectLinks(snap)
	if links[0].URL != "https://box.ts.net:9420" {
		t.Errorf("first link is %q, want the tailnet address", links[0].URL)
	}
}
