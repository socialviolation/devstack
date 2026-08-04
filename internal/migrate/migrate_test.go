package migrate

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func one() []workspace.Workspace {
	return []workspace.Workspace{{Name: "navexa", Path: "/dev/navexa"}}
}

// counter builds a patch that reports itself pending until it has run once, so
// a test can see whether the framework ran it or skipped it.
func counter(id string, pending *bool, runs *int) Patch {
	return Patch{
		ID:    id,
		Title: "a patch",
		Detect: func(*workspace.Workspace) (bool, string, error) {
			return *pending, "nothing is pending", nil
		},
		Run: func(ws *workspace.Workspace) (Result, error) {
			*runs++
			*pending = false
			return Result{Changed: true, Lines: []string{"    did the work"}}, nil
		},
		Next: func(res []Result) []string { return []string{id + ": do the next thing"} },
	}
}

// A pending patch runs, and the run is recorded. The record is what keeps a
// patch whose effect is invisible on disk from running twice.
func TestPendingPatchRunsAndIsRecorded(t *testing.T) {
	isolate(t)
	pending, runs := true, 0

	if err := Apply(&strings.Builder{}, []Patch{counter("p1", &pending, &runs)}, one()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if runs != 1 {
		t.Fatalf("the patch ran %d times, want 1", runs)
	}

	recs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != "p1" || recs[0].Workspace != "navexa" {
		t.Fatalf("records = %+v, want one for p1 in navexa", recs)
	}
	if recs[0].AppliedAt.IsZero() {
		t.Error("the record carries no time")
	}
}

// A recorded patch does not run again, even where its detector still says
// pending. That is the whole point of the record: a build nobody wants repeated.
func TestRecordedPatchDoesNotRunAgain(t *testing.T) {
	isolate(t)
	pending, runs := true, 0
	p := counter("p1", &pending, &runs)

	if err := Apply(&strings.Builder{}, []Patch{p}, one()); err != nil {
		t.Fatalf("first Apply() = %v", err)
	}
	pending = true

	var b strings.Builder
	if err := Apply(&b, []Patch{p}, one()); err != nil {
		t.Fatalf("second Apply() = %v", err)
	}
	if runs != 1 {
		t.Fatalf("the patch ran %d times, want 1: the record must stop the second run:\n%s", runs, b.String())
	}
	if !strings.Contains(b.String(), "applied already") {
		t.Errorf("the report never says the patch was applied already:\n%s", b.String())
	}
}

// A migration is one-off. After it is applied, it never reads as pending again,
// whatever the detector says. A detector that speaks for state that keeps
// changing made every new service in a workspace turn an applied migration back
// into a pending one, which is not a thing a migration can be.
func TestAnAppliedPatchNeverReportsPendingAgain(t *testing.T) {
	isolate(t)
	pending, runs := true, 0
	p := counter("p1", &pending, &runs)

	if err := Apply(&strings.Builder{}, []Patch{p}, one()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	pending = true

	st, err := List([]Patch{p}, one())
	if err != nil {
		t.Fatal(err)
	}
	if st[0].Pending() {
		t.Fatalf("an applied migration reads as pending again: %+v", st[0].Rows)
	}

	var b strings.Builder
	WriteList(&b, st)
	if strings.Contains(b.String(), "pending:") || strings.Contains(b.String(), "pending again") {
		t.Errorf("the list calls an applied migration pending:\n%s", b.String())
	}
	if !strings.Contains(b.String(), "applied on") {
		t.Errorf("the list never says when the migration was applied:\n%s", b.String())
	}
	if runs != 1 {
		t.Errorf("the patch ran %d times, want 1", runs)
	}
}

// A patch that applies to nothing is the normal case on a machine that is up to
// date. It is reported, and it is not an error.
func TestPatchThatAppliesToNothingIsNotAnError(t *testing.T) {
	isolate(t)
	ran := false
	p := Patch{
		ID:     "p1",
		Title:  "a patch",
		Detect: func(*workspace.Workspace) (bool, string, error) { return false, "every file is clean", nil },
		Run: func(*workspace.Workspace) (Result, error) {
			ran = true
			return Result{Changed: true}, nil
		},
		Next: func([]Result) []string { return []string{"do the next thing"} },
	}

	var b strings.Builder
	if err := Apply(&b, []Patch{p}, one()); err != nil {
		t.Fatalf("Apply() = %v, want no error", err)
	}
	if ran {
		t.Error("a patch that is not pending must not run")
	}
	got := b.String()
	if !strings.Contains(got, "nothing to do: every file is clean") {
		t.Errorf("the report never says why there is nothing to do:\n%s", got)
	}
	if strings.Contains(got, "do the next thing") {
		t.Errorf("a patch that changed nothing must not print its next action:\n%s", got)
	}
	if !strings.Contains(got, "Every migration is applied") {
		t.Errorf("a run that changed nothing must say so:\n%s", got)
	}
}

// One patch failing must not strand the patches after it. Both are reported.
func TestFailingPatchDoesNotStopTheNextOne(t *testing.T) {
	isolate(t)
	pending, runs := true, 0
	bad := Patch{
		ID:     "p-bad",
		Title:  "a patch that fails",
		Detect: func(*workspace.Workspace) (bool, string, error) { return true, "", nil },
		Run: func(*workspace.Workspace) (Result, error) {
			return Result{}, errBoom
		},
		Next: func([]Result) []string { return []string{"p-bad: do the next thing"} },
	}

	var b strings.Builder
	err := Apply(&b, []Patch{bad, counter("p-good", &pending, &runs)}, one())
	if err == nil {
		t.Fatal("a failed patch must be reported as an error")
	}
	if !strings.Contains(err.Error(), "p-bad") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("the error does not name the patch and the cause: %v", err)
	}
	if runs != 1 {
		t.Errorf("the patch after the failure ran %d times, want 1", runs)
	}
	got := b.String()
	if !strings.Contains(got, "failed: boom") {
		t.Errorf("the report never states the failure:\n%s", got)
	}
	if !strings.Contains(got, "p-good: do the next thing") {
		t.Errorf("the report never carries the next action of the patch that ran:\n%s", got)
	}
	if strings.Contains(got, "p-bad: do the next thing") {
		t.Errorf("a patch that changed nothing must not print its next action:\n%s", got)
	}
	recs, _ := Load()
	if len(recs) != 1 || recs[0].ID != "p-good" {
		t.Errorf("records = %+v, want the one patch that changed something", recs)
	}
}

// The closing note is the last thing an agent reads, so it must carry the next
// action of every patch that changed something, and of no other patch.
func TestTheNoteCarriesOnlyTheNextActionsOfPatchesThatChanged(t *testing.T) {
	isolate(t)
	pending, runs := true, 0
	quiet := Patch{
		ID:     "p-quiet",
		Title:  "a patch with nothing to do",
		Detect: func(*workspace.Workspace) (bool, string, error) { return false, "already done", nil },
		Run:    func(*workspace.Workspace) (Result, error) { return Result{}, nil },
		Next:   func([]Result) []string { return []string{"p-quiet: do the next thing"} },
	}

	var b strings.Builder
	if err := Apply(&b, []Patch{quiet, counter("p-loud", &pending, &runs)}, one()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	got := b.String()

	note := got[strings.Index(got, "NEXT"):]
	if !strings.Contains(note, "p-loud: do the next thing") {
		t.Errorf("the note drops the next action of the patch that ran:\n%s", note)
	}
	if strings.Contains(note, "p-quiet") {
		t.Errorf("the note carries the next action of a patch that did nothing:\n%s", note)
	}
	if strings.Index(got, "did the work") > strings.Index(got, "NEXT") {
		t.Errorf("the note must print last, after the per-patch report:\n%s", got)
	}
}

// A patch's next action must name the workspaces it changed, so the reader knows
// where to act.
func TestNextReceivesOneResultPerChangedWorkspace(t *testing.T) {
	isolate(t)
	var seen []string
	p := Patch{
		ID:     "p1",
		Title:  "a patch",
		Detect: func(ws *workspace.Workspace) (bool, string, error) { return ws.Name != "shop", "", nil },
		Run:    func(*workspace.Workspace) (Result, error) { return Result{Changed: true}, nil },
		Next: func(res []Result) []string {
			for _, r := range res {
				seen = append(seen, r.Workspace)
			}
			return []string{"done"}
		},
	}

	all := []workspace.Workspace{{Name: "navexa"}, {Name: "shop"}, {Name: "tsfc"}}
	if err := Apply(&strings.Builder{}, []Patch{p}, all); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if strings.Join(seen, ",") != "navexa,tsfc" {
		t.Errorf("Next saw %v, want the two workspaces the patch changed", seen)
	}
}

// --list has to show both halves of the truth: what has been applied, and what
// is still pending.
func TestListShowsAppliedAndPending(t *testing.T) {
	isolate(t)
	pending, runs := true, 0
	applied := counter("p-applied", &pending, &runs)
	if err := Apply(&strings.Builder{}, []Patch{applied}, one()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	open := Patch{
		ID:     "p-open",
		Title:  "still to do",
		Detect: func(*workspace.Workspace) (bool, string, error) { return true, "2 files hold a block", nil },
		Run:    func(*workspace.Workspace) (Result, error) { return Result{Changed: true}, nil },
	}

	st, err := List([]Patch{applied, open}, one())
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 2 {
		t.Fatalf("List() returned %d patches, want 2", len(st))
	}
	if st[0].Pending() {
		t.Errorf("the applied patch reads as pending: %+v", st[0].Rows)
	}
	if st[0].Rows[0].AppliedAt.IsZero() {
		t.Errorf("the applied patch carries no time: %+v", st[0].Rows)
	}
	if !st[1].Pending() || st[1].Rows[0].Why != "2 files hold a block" {
		t.Errorf("the pending patch reads as %+v", st[1].Rows)
	}
	if runs != 1 {
		t.Error("List must change nothing, and it ran a patch")
	}
}

// A detector that fails must not be read as "nothing to do", or a broken
// workspace goes quiet.
func TestDetectFailureIsReportedAndDoesNotRun(t *testing.T) {
	isolate(t)
	ran := false
	p := Patch{
		ID:     "p1",
		Title:  "a patch",
		Detect: func(*workspace.Workspace) (bool, string, error) { return false, "", errBoom },
		Run: func(*workspace.Workspace) (Result, error) {
			ran = true
			return Result{Changed: true}, nil
		},
	}

	var b strings.Builder
	err := Apply(&b, []Patch{p}, one())
	if err == nil {
		t.Fatal("a detector that fails must be reported")
	}
	if ran {
		t.Error("a patch whose detector failed must not run")
	}
	if !strings.Contains(b.String(), "can not read this workspace") {
		t.Errorf("the report never names the workspace it can not read:\n%s", b.String())
	}
}

var errBoom = boom{}

type boom struct{}

func (boom) Error() string { return "boom" }
