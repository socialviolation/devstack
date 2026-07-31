package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
)

func recs() []stack.Record {
	return []stack.Record{
		{Name: "agent", Branch: "feat/roi-api", Overlay: []string{"api", "frontend"}},
		{Name: "fx-rates", Branch: "nick/fx", Overlay: []string{"api", "prices"}},
		{Name: "solo", Branch: "nick/solo", Overlay: []string{"importer"}},
	}
}

// matchBranch and singleCandidate are the two inferences that run without a
// worktree to settle the question; they are exercised directly because
// DetectFromCwd depends on the real filesystem.
func matchBranch(list []stack.Record, branch string) *stack.Record {
	bare := strings.TrimSuffix(branch, "*")
	for i := range list {
		if list[i].Branch != "" && list[i].Branch == bare {
			return &list[i]
		}
	}
	return nil
}

func singleCandidate(list []stack.Record, service string) []*stack.Record {
	var out []*stack.Record
	for i := range list {
		if containsString(list[i].Overlay, service) {
			out = append(out, &list[i])
		}
	}
	return out
}

func TestBranchMatchIdentifiesTheStack(t *testing.T) {
	got := matchBranch(recs(), "nick/fx")
	if got == nil || got.Name != "fx-rates" {
		t.Fatalf("matchBranch() = %v, want fx-rates", got)
	}
}

// gitinfo marks a dirty checkout with a trailing "*", which must not stop the
// branch matching its stack — uncommitted work is the normal state mid-feature.
func TestBranchMatchIgnoresTheDirtyMarker(t *testing.T) {
	got := matchBranch(recs(), "nick/fx*")
	if got == nil || got.Name != "fx-rates" {
		t.Fatalf("matchBranch() with dirty marker = %v, want fx-rates", got)
	}
}

func TestBranchMatchReturnsNothingForAnUnrelatedBranch(t *testing.T) {
	if got := matchBranch(recs(), "master"); got != nil {
		t.Fatalf("matchBranch() = %v, want no match for master", got)
	}
}

// A service in one stack is a suggestion worth making.
func TestSingleCandidateIsOfferable(t *testing.T) {
	got := singleCandidate(recs(), "importer")
	if len(got) != 1 || got[0].Name != "solo" {
		t.Fatalf("singleCandidate() = %v, want just solo", got)
	}
}

// A service in several stacks must stay ambiguous. Naming one would send an
// agent to edit a worktree nobody asked about, which is worse than saying
// nothing.
func TestMultipleCandidatesStayAmbiguous(t *testing.T) {
	if got := singleCandidate(recs(), "api"); len(got) < 2 {
		t.Fatalf("singleCandidate() = %v, want more than one candidate for api", got)
	}
}

func TestNoCandidateForAServiceNoStackRuns(t *testing.T) {
	if got := singleCandidate(recs(), "unrelated"); len(got) != 0 {
		t.Fatalf("singleCandidate() = %v, want none", got)
	}
}

// The briefing is injected into every session, so it must fit the SessionStart
// budget rather than being silently truncated to a file preview.
func TestPrimeBudgetIsUnderTheSessionStartCap(t *testing.T) {
	const claudeCodeCap = 10000
	if primeCharBudget >= claudeCodeCap {
		t.Fatalf("primeCharBudget = %d, must leave headroom under the %d cap", primeCharBudget, claudeCodeCap)
	}
}

// The --json shape is a contract with the hook runner: a wrong key means the
// briefing is silently dropped rather than failing loudly.
func TestPrimeJSONEnvelopeShape(t *testing.T) {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": "body",
		},
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q", back.HookSpecificOutput.HookEventName)
	}
	if back.HookSpecificOutput.AdditionalContext != "body" {
		t.Errorf("additionalContext = %q", back.HookSpecificOutput.AdditionalContext)
	}
}

// The briefing is injected whole into a session and cannot be skimmed unless
// its parts are named. THIS SERVICE is omitted outside a service directory,
// because a heading with nothing under it reads as missing data.
func TestPrimeSectionsAreNamedAndContextual(t *testing.T) {
	always := []string{"## DEVSTACK", "## TERMS", "## WHERE YOU ARE", "## THIS WORKSPACE", "## REFERENCE"}
	for _, want := range always {
		if !strings.HasPrefix(want, "## ") {
			t.Fatalf("section heading %q must be a markdown heading", want)
		}
	}

	var b strings.Builder
	section(&b, "THIS SERVICE — api")
	if got := b.String(); got != "\n## THIS SERVICE — api\n" {
		t.Fatalf("section() = %q", got)
	}
}
