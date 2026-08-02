package selfcheck

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The comparison is read from the branch's side, and the words have to survive
// that: comparing main...<build>, a build older than the branch comes back
// "behind", which is also what a reader means by it.
func TestInterpretMapsTheHostsWords(t *testing.T) {
	for _, tc := range []struct {
		status   string
		ahead    int
		behind   int
		want     Status
		wantBhnd int
	}{
		{"identical", 0, 0, StatusCurrent, 0},
		{"behind", 0, 12, StatusBehind, 12},
		{"ahead", 3, 0, StatusAhead, 0},
		{"diverged", 2, 5, StatusDiverged, 5},
		{"nonsense", 0, 0, StatusUnknown, 0},
	} {
		got := Interpret(tc.status, tc.ahead, tc.behind)
		if got.Status != tc.want || got.BehindBy != tc.wantBhnd {
			t.Errorf("Interpret(%q, %d, %d) = %+v, want %s behind=%d", tc.status, tc.ahead, tc.behind, got, tc.want, tc.wantBhnd)
		}
	}
}

// Nothing is said when the build is current or the answer never arrived. A line
// printed on every command is a line readers learn to skip, which would cost the
// one case that matters its only chance of being read.
func TestDescribeIsSilentWhenThereIsNothingToSay(t *testing.T) {
	for _, s := range []Status{StatusCurrent, StatusUnknown} {
		if got := (Result{Status: s}).Describe("example.com/x"); got != "" {
			t.Errorf("Describe(%s) = %q, want silence", s, got)
		}
	}
}

func TestDescribeNamesTheUpdateAndCountsCorrectly(t *testing.T) {
	got := (Result{Status: StatusBehind, BehindBy: 1}).Describe("github.com/o/r")
	if !strings.Contains(got, "1 commit behind") {
		t.Errorf("a single commit must not read as \"1 commits\": %q", got)
	}
	if !strings.Contains(got, "go install github.com/o/r@latest") {
		t.Errorf("the line must carry the command that fixes it: %q", got)
	}

	if got := (Result{Status: StatusBehind, BehindBy: 12}).Describe("github.com/o/r"); !strings.Contains(got, "12 commits behind") {
		t.Errorf("Describe() = %q", got)
	}
}

// A build from an unpushed commit is the normal state while developing devstack
// itself. It is reported as what it is, and never as an update to install —
// "updating" would move the binary backwards onto the published branch.
func TestLocalBuildIsReportedAndNeverPromptsAnUpdate(t *testing.T) {
	got := (Result{Status: StatusLocal}).Describe("github.com/o/r")
	if !strings.Contains(got, "local build") {
		t.Errorf("Describe(local) = %q", got)
	}
	if strings.Contains(got, "go install") {
		t.Errorf("a local build must not be told to install an older published commit: %q", got)
	}
}

func TestRepoSlugAcceptsOnlyTheHostItCanQuery(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/socialviolation/devstack":     "socialviolation/devstack",
		"github.com/socialviolation/devstack/v2":  "socialviolation/devstack",
		"gitlab.com/socialviolation/devstack":     "",
		"github.com/socialviolation":              "",
		"":                                        "",
		"example.com/socialviolation/devstack/v3": "",
	} {
		if got := repoSlug(in); got != want {
			t.Errorf("repoSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every failure has to resolve to silence. This is a courtesy check, and it must
// never be why a command fails, hangs, or prints something alarming.
func TestEveryFailureResolvesToSilence(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"rate limited": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) },
		"garbage body": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) },
	} {
		srv := httptest.NewServer(handler)
		orig := compareAPI
		compareAPI = srv.URL + "/%s/%s/%s"
		got := compare("github.com/o/r", "deadbeef")
		compareAPI = orig
		srv.Close()

		if got.Status != StatusUnknown {
			t.Errorf("%s: got %s, want %s", name, got.Status, StatusUnknown)
		}
		if line := got.Describe("github.com/o/r"); line != "" {
			t.Errorf("%s: printed %q, want silence", name, line)
		}
	}
}

// A 404 is not a failure. It is the host saying it has never seen this commit,
// which is exactly what a local build looks like from the outside.
func TestNotFoundIsReadAsALocalBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := compareAPI
	compareAPI = srv.URL + "/%s/%s/%s"
	defer func() { compareAPI = orig }()

	if got := compare("github.com/o/r", "deadbeef"); got.Status != StatusLocal {
		t.Errorf("compare() = %s, want %s", got.Status, StatusLocal)
	}
}

func TestBehindIsReadFromALiveResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"behind","ahead_by":0,"behind_by":7}`))
	}))
	defer srv.Close()

	orig := compareAPI
	compareAPI = srv.URL + "/%s/%s/%s"
	defer func() { compareAPI = orig }()

	got := compare("github.com/o/r", "deadbeef")
	if got.Status != StatusBehind || got.BehindBy != 7 {
		t.Fatalf("compare() = %+v, want behind by 7", got)
	}
}

// A cached answer belongs to the binary that asked for it. Reusing it after an
// upgrade would report the old build's staleness as the new build's.
func TestCacheIsRejectedWhenTheBinaryChanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store(Result{Status: StatusBehind, BehindBy: 4, Revision: "oldrev"})

	if _, ok := Cached("newrev"); ok {
		t.Error("a result cached for another revision must not be reused")
	}
	if got, ok := Cached("oldrev"); !ok || got.BehindBy != 4 {
		t.Errorf("Cached(oldrev) = %+v, %v", got, ok)
	}
}
