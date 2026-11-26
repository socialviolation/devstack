package stack

import (
	"strings"
	"testing"
	"time"
)

func TestAdoptClearsOwnerAndKeepsTheStopTime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := upsertStack(Record{Name: "agent-x", Base: "navexa", Owner: "alice", StopAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	rec, adopted, err := Adopt("navexa", "AGENT-X", now)
	if err != nil {
		t.Fatal(err)
	}
	if !adopted {
		t.Fatal("Adopt reported no change for an agent stack")
	}
	stored, err := FindStack("navexa", "agent-x")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []Record{rec, *stored} {
		if r.Owner != "" || !r.StopAt.Equal(now.Add(time.Hour)) {
			t.Errorf("adopted stack has owner %q and stop time %v, want no owner and the same stop time", r.Owner, r.StopAt)
		}
		if r.AdoptedFrom != "alice" || !r.AdoptedAt.Equal(now) {
			t.Errorf("adopted stack records from %q at %v, want alice at %v", r.AdoptedFrom, r.AdoptedAt, now)
		}
	}
}

func TestAdoptLeavesAnOwnerStackAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	created := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	if err := upsertStack(Record{Name: "perf", Base: "navexa", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}

	rec, adopted, err := Adopt("navexa", "perf", created.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if adopted || rec.AdoptedFrom != "" || !rec.AdoptedAt.IsZero() {
		t.Errorf("Adopt changed an owner stack: adopted=%v from=%q at=%v", adopted, rec.AdoptedFrom, rec.AdoptedAt)
	}
}

func TestAdoptUnknownStackListsAgentStacks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, r := range []Record{
		{Name: "agent-a", Base: "navexa", Owner: "alice"},
		{Name: "perf", Base: "navexa"},
	} {
		if err := upsertStack(r); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := Adopt("navexa", "agent-gone", time.Now())
	if err == nil {
		t.Fatal("Adopt of an unknown stack returned no error")
	}
	if !strings.Contains(err.Error(), `"agent-gone" not found`) || !strings.Contains(err.Error(), "agent-a (owner alice)") || strings.Contains(err.Error(), "perf") {
		t.Errorf("error = %q, want the unknown name and agent-a alone", err)
	}
}
