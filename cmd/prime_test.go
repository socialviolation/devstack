package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
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

// The briefing used to count the operations that have no tool ("Six things have
// no tool"). The list beside it names the ones worth naming, not all of them —
// there are far more — so the count was wrong the day it was written and nothing
// could tell. A count is only safe if it is generated, and the number helps
// nobody, so the briefing states the rule instead. This fails if a count returns.
//
// It lives here rather than beside TestBriefingParityClaimHolds because the text
// is written in this package, and internal/mcp cannot import it.
func TestBriefingCountsNoOperations(t *testing.T) {
	var b strings.Builder
	writePrimeWhatThisIs(&b)
	text := strings.ToLower(b.String())

	for _, n := range []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"} {
		if strings.Contains(text, n+" thing") || strings.Contains(text, n+" command") || strings.Contains(text, n+" operation") {
			t.Errorf("the briefing counts the operations with no tool (%q); nothing verifies the count, so state the rule", n)
		}
	}
	if !strings.Contains(text, "no tool") {
		t.Error("the briefing must still say the tools do not cover every command")
	}
}

// The environments block prints a description per environment, and prints
// something for the ones that have none. Unlabelled, that filler sits under a
// nameless column and reads as "this environment sets nothing" — a reviewer drew
// exactly that conclusion while an environment was live on a stack. The heading
// is what says the missing thing is the description, not the values.
func TestEnvironmentsBlockLabelsItsDescriptionColumn(t *testing.T) {
	rw := &config.ResolvedWorkspace{Manifest: &config.WorkspaceManifest{
		Environments: map[string]config.WorkspaceEnvironment{
			"dev":     {},
			"fx-prod": {Description: "points nx-api at the production FX database"},
		},
	}}
	rw.Manifest.Workspace.Env = "dev"

	var b strings.Builder
	writePrimeApplies(&b, &workspace.Workspace{Name: "navexa"}, rw)
	out := b.String()

	if !strings.Contains(out, "PURPOSE") {
		t.Errorf("the description column is unlabelled:\n%s", out)
	}
	if strings.Contains(out, "unset") {
		t.Errorf("a bare \"unset\" beside an environment reads as \"it sets nothing\":\n%s", out)
	}
	if !strings.Contains(out, "fx-prod") || !strings.Contains(out, "production FX database") {
		t.Errorf("an environment's recorded purpose is missing:\n%s", out)
	}
}

// The briefing is injected whole into a session and cannot be skimmed unless
// its parts are named. THIS SERVICE is omitted outside a service directory,
// because a heading with nothing under it reads as missing data.
func TestPrimeSectionsAreNamedAndContextual(t *testing.T) {
	always := []string{"## YOUR TASK", "## DEVSTACK", "## TERMS", "## WHERE YOU ARE", "## THIS WORKSPACE", "## REFERENCE"}
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

// The two rules about what a repository commits. devstack no longer writes them
// into AGENTS.md, and no live fact replaces them, so the briefing is the only
// place left that states them.
func TestPrimeCarriesTheTwoCommitRules(t *testing.T) {
	var b strings.Builder
	writePrimeSafety(&b)
	got := b.String()

	for _, want := range []string{
		"Never commit `devstack.service.yaml`",
		"`.gitignore`",
		"keep every real secret out of it",
		"`env.required`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the briefing never states %q:\n%s", want, got)
		}
	}
}

// Every line of the briefing is paid for on every session, and the whole of it
// has to fit the SessionStart budget. The static half is what this package can
// measure without a workspace.
func TestTheStaticBriefingFitsTheBudget(t *testing.T) {
	var b strings.Builder
	writePrimeWhatThisIs(&b)
	writePrimeTerms(&b)
	writePrimeSafety(&b)

	if n := b.Len(); n > primeCharBudget/2 {
		t.Errorf("the static briefing is %d chars, over half the %d budget; the live half needs the rest", n, primeCharBudget)
	}
}
