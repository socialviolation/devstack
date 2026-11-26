package stack

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/workspace"
)

func fixClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := Now
	Now = func() time.Time { return now }
	t.Cleanup(func() { Now = prev })
}

func writeStore(t *testing.T, raw string) {
	t.Helper()
	dir := workspace.DataDir("navexa")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"stacks.json", []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestStoreReadsRecordsWithoutOwnerOrStopTime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeStore(t, `[{"name":"perf","base":"navexa","root":"/r","branch":"perf","overlay":["api"],"worktrees":{},"ports":{}}]`)
	rec, err := FindStack("navexa", "perf")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Owner != "" || !rec.StopAt.IsZero() {
		t.Errorf("old record parsed with owner %q and stop time %v, want neither", rec.Owner, rec.StopAt)
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"owner", "expires_at", "stop_at", "stopped_by", "stopped_at"} {
		if strings.Contains(string(data), key) {
			t.Errorf("a record with no %s writes it anyway: %s", key, data)
		}
	}
}

func TestARecordWithNoStopTimeStopsAWindowAfterCreation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeStore(t, `[{"name":"perf","base":"navexa","active":true,"worktrees":{},"ports":{},"overlay":[],"created_at":"2026-01-02T03:04:05Z"}]`)
	rec, err := FindStack("navexa", "perf")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !rec.StopAt.Equal(created.Add(StopAfter)) {
		t.Errorf("stop time = %v, want %v after creation", rec.StopAt, StopAfter)
	}
	due, err := Due("navexa", created.Add(StopAfter))
	if err != nil || len(due) != 1 {
		t.Errorf("Due = %v, %v; want the old up stack", due, err)
	}
}

func TestLegacyExpiresAtBecomesTheStopTime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeStore(t, `[{"name":"agent-x","base":"navexa","owner":"alice","expires_at":"2026-11-26T09:00:00Z","worktrees":{},"ports":{},"overlay":[]},
{"name":"agent-y","base":"navexa","owner":"alice","expires_at":"2026-11-26T09:00:00Z","stop_at":"2026-11-27T09:00:00Z","worktrees":{},"ports":{},"overlay":[]}]`)
	want := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

	rec, err := FindStack("navexa", "agent-x")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.StopAt.Equal(want) {
		t.Errorf("stop time = %v, want the old expiry %v", rec.StopAt, want)
	}
	if y, _ := FindStack("navexa", "agent-y"); !y.StopAt.Equal(want.Add(24 * time.Hour)) {
		t.Errorf("a record with both keys took the old expiry: %v", y.StopAt)
	}

	if err := SetNote("navexa", "agent-x", "keep"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(workspace.DataDir("navexa") + "stacks.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "expires_at") || !strings.Contains(string(data), `"stop_at": "2026-11-26T09:00:00Z"`) {
		t.Errorf("the next write did not move expires_at to stop_at:\n%s", data)
	}
}

func TestDueIsOnlyUpStacksPastTheirStopTime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for _, r := range []Record{
		{Name: "old", Base: "navexa", Active: true, StopAt: now.Add(-time.Minute)},
		{Name: "due", Base: "navexa", Active: true, StopAt: now},
		{Name: "new", Base: "navexa", Active: true, StopAt: now.Add(time.Minute)},
		{Name: "down", Base: "navexa", StopAt: now.Add(-time.Hour)},
		{Name: "nostop", Base: "navexa", Active: true},
	} {
		if err := upsertStack(r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Due("navexa", now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "old,due" {
		t.Errorf("Due = %v, want [old due]", names)
	}
}

func TestExtendAddsToTheLaterOfStopTimeAndNow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	created := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	now := created.Add(10 * time.Hour)
	for _, r := range []Record{
		{Name: "ahead", Base: "navexa", CreatedAt: created, StopAt: created.Add(24 * time.Hour)},
		{Name: "behind", Base: "navexa", CreatedAt: created, StopAt: created.Add(time.Hour)},
	} {
		if err := upsertStack(r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Extend("navexa", "ahead", 12*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := created.Add(36 * time.Hour); !got.Equal(want) {
		t.Errorf("extend a future stop time by 12h = %v, want %v", got, want)
	}
	got, err = Extend("navexa", "behind", 12*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(12 * time.Hour); !got.Equal(want) {
		t.Errorf("extend a past stop time by 12h = %v, want %v", got, want)
	}

	for range 4 {
		if got, err = Extend("navexa", "ahead", MaxExtend, now); err != nil {
			t.Fatal(err)
		}
	}
	if want := created.Add(132 * time.Hour); !got.Equal(want) {
		t.Errorf("four more 24h extends = %v, want %v with no lifetime cap", got, want)
	}
	rec, _ := FindStack("navexa", "ahead")
	if !rec.StopAt.Equal(got) {
		t.Errorf("stored stop time = %v, want %v", rec.StopAt, got)
	}

	if _, err := Extend("navexa", "ahead", MaxExtend+time.Hour, now); err == nil || !strings.Contains(err.Error(), "24 hours or less") {
		t.Errorf("extend by 25h: err = %v, want a refusal", err)
	}
	if after, _ := FindStack("navexa", "ahead"); !after.StopAt.Equal(got) {
		t.Errorf("a refused extend changed the stop time to %v", after.StopAt)
	}
}

func TestMarkUpSetsANewStopTimeAndClearsTheStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	fixClock(t, now)
	if err := upsertStack(Record{Name: "perf", Base: "navexa", StopAt: now.Add(-48 * time.Hour), StoppedBy: StoppedBySweep, StoppedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if err := MarkUp("navexa", "perf"); err != nil {
		t.Fatal(err)
	}
	rec, _ := FindStack("navexa", "perf")
	if !rec.Active || !rec.StopAt.Equal(now.Add(StopAfter)) || rec.StoppedBy != "" || !rec.StoppedAt.IsZero() {
		t.Errorf("after up: active=%v stop=%v by=%q at=%v, want active, stop at now+%v, no stop record", rec.Active, rec.StopAt, rec.StoppedBy, rec.StoppedAt, StopAfter)
	}

	later := now.Add(2 * time.Hour)
	fixClock(t, later)
	if err := MarkDown("navexa", "perf", StoppedByUser); err != nil {
		t.Fatal(err)
	}
	rec, _ = FindStack("navexa", "perf")
	if rec.Active || rec.StoppedBy != StoppedByUser || !rec.StoppedAt.Equal(later) {
		t.Errorf("after down: active=%v by=%q at=%v, want down by user at %v", rec.Active, rec.StoppedBy, rec.StoppedAt, later)
	}
}

func TestCreateSetsAStopTimeOnOwnerAndAgentStacks(t *testing.T) {
	base := newBase(t)
	noDaemon(t)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	fixClock(t, now)

	for _, in := range []CreateInput{
		{Base: base, Name: "agent-feat", Repos: []string{"backend"}, Owner: "alice"},
		{Base: base, Name: "mine", Repos: []string{"backend"}},
	} {
		if _, err := Create(in); err != nil {
			t.Fatalf("Create %s: %v", in.Name, err)
		}
		rec, err := FindStack(base.Name, in.Name)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Owner != in.Owner {
			t.Errorf("%s: owner = %q, want %q", in.Name, rec.Owner, in.Owner)
		}
		if !rec.CreatedAt.Equal(now) || !rec.StopAt.Equal(now.Add(StopAfter)) {
			t.Errorf("%s: created %v, stops %v, want stop %v after creation", in.Name, rec.CreatedAt, rec.StopAt, StopAfter)
		}
	}
}

func TestDeadlineText(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local)
	for _, c := range []struct {
		info StackInfo
		want string
	}{
		{StackInfo{Status: StatusUp, StopAt: now.Add(2 * time.Hour)}, "up — stops 14:00 today"},
		{StackInfo{Status: StatusUp, StopAt: now.Add(22 * time.Hour)}, "up — stops 10:00 tomorrow"},
		{StackInfo{Status: StatusUp, StopAt: now.Add(72 * time.Hour)}, "up — stops 2026-11-28 12:00"},
		{StackInfo{Status: StatusDown, StoppedBy: StoppedBySweep, StoppedAt: now}, "stopped by devstack 2026-11-25 12:00"},
		{StackInfo{Status: StatusDown, StoppedBy: StoppedByUser, StoppedAt: now}, ""},
		{StackInfo{Status: StatusUp}, ""},
	} {
		if got := c.info.Deadline(now); got != c.want {
			t.Errorf("Deadline(%+v) = %q, want %q", c.info, got, c.want)
		}
	}
}
