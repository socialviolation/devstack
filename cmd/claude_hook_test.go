package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", path, err, data)
	}
	return out
}

// primeMatchers lists the matchers whose hook list runs devstack prime.
func primeMatchers(t *testing.T, settings map[string]any) []string {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	sessionStart, _ := hooks["SessionStart"].([]any)
	var out []string
	for _, raw := range sessionStart {
		entry, _ := raw.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); isPrimeCommand(cmd) {
				m, _ := entry["matcher"].(string)
				out = append(out, m)
			}
		}
	}
	return out
}

func TestClaudeHookWritesEveryMatcher(t *testing.T) {
	dir := t.TempDir()
	changed, err := ensureClaudeSessionHook(dir)
	if err != nil {
		t.Fatalf("ensureClaudeSessionHook: %v", err)
	}
	if !changed {
		t.Fatal("nothing was written into a directory with no settings file")
	}

	got := primeMatchers(t, readSettings(t, filepath.Join(dir, claudeSettingsRel)))
	if strings.Join(got, ",") != strings.Join(primeHookMatchers, ",") {
		t.Fatalf("briefed matchers = %v, want %v", got, primeHookMatchers)
	}
	if !containsString(got, "compact") {
		t.Error("compact is the matcher that matters most, because compaction is when the landscape is lost")
	}
}

// The file is the user's, and devstack adds one hook to it. Every other key and
// every hook they already declared survives.
func TestClaudeHookMergesIntoAnExistingSettingsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeSettingsRel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `{
  "model": "opus",
  "permissions": {"allow": ["Bash(git status)"]},
  "hooks": {
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "./bin/greet"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "./bin/done"}]}
    ]
  }
}
`)

	if _, err := ensureClaudeSessionHook(dir); err != nil {
		t.Fatalf("ensureClaudeSessionHook: %v", err)
	}
	settings := readSettings(t, path)

	if settings["model"] != "opus" {
		t.Errorf("model was lost: %#v", settings["model"])
	}
	if _, ok := settings["permissions"]; !ok {
		t.Error("permissions were lost")
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if _, ok := hooks["Stop"]; !ok {
		t.Error("the Stop hooks of the user were lost")
	}

	raw, _ := json.Marshal(settings)
	if !strings.Contains(string(raw), "./bin/greet") {
		t.Errorf("the existing startup hook of the user was lost:\n%s", raw)
	}
	if got := primeMatchers(t, settings); len(got) != len(primeHookMatchers) {
		t.Errorf("briefed matchers = %v, want all of %v", got, primeHookMatchers)
	}
}

func TestClaudeHookIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := ensureClaudeSessionHook(dir); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := readString(t, filepath.Join(dir, claudeSettingsRel))

	changed, err := ensureClaudeSessionHook(dir)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if changed {
		t.Error("the second run reported a change, so it wrote a duplicate")
	}
	if second := readString(t, filepath.Join(dir, claudeSettingsRel)); first != second {
		t.Fatalf("not byte-identical on the second run:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// A user who already wired prime themselves keeps their own entry, whatever
// flags they gave it.
func TestClaudeHookRespectsAHandWrittenPrimeEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeSettingsRel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"devstack prime --json | tee /tmp/brief"}]}]}}`)

	if _, err := ensureClaudeSessionHook(dir); err != nil {
		t.Fatalf("ensureClaudeSessionHook: %v", err)
	}
	got := readString(t, path)
	if !strings.Contains(got, "tee /tmp/brief") {
		t.Fatalf("the hand-written entry was replaced:\n%s", got)
	}
	if strings.Count(got, "devstack prime") != len(primeHookMatchers) {
		t.Fatalf("one prime entry per matcher and no more:\n%s", got)
	}
}

// A settings file devstack cannot parse is a file devstack must not rewrite.
func TestClaudeHookRefusesUnparseableSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeSettingsRel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	broken := "{ this is not json\n"
	writeFile(t, path, broken)

	if _, err := ensureClaudeSessionHook(dir); err == nil {
		t.Fatal("ensureClaudeSessionHook() = nil for unparseable settings, want an error")
	}
	if got := readString(t, path); got != broken {
		t.Fatalf("the file was rewritten anyway:\n%s", got)
	}
}

// Running init --all twice must leave one entry per matcher, in the workspace
// root and in every service.
func TestRunInitAllWritesTheHookOnceWithTheFlag(t *testing.T) {
	baseRoot, _, baseServiceDir := setupBaseAndSiblingStack(t)
	t.Chdir(baseRoot)

	for i := 0; i < 2; i++ {
		if err := runInitAll(true); err != nil {
			t.Fatalf("runInitAll run %d: %v", i+1, err)
		}
	}

	for _, dir := range []string{baseRoot, baseServiceDir} {
		path := filepath.Join(dir, claudeSettingsRel)
		got := readString(t, path)
		if n := strings.Count(got, primeHookCommand); n != len(primeHookMatchers) {
			t.Errorf("%s holds %d prime entries, want %d", path, n, len(primeHookMatchers))
		}
	}
}

// Without the flag devstack writes nothing into .claude, because that file is
// committed and shared with a team.
func TestRunInitAllLeavesClaudeSettingsAloneByDefault(t *testing.T) {
	baseRoot, _, baseServiceDir := setupBaseAndSiblingStack(t)
	t.Chdir(baseRoot)

	if err := runInitAll(false); err != nil {
		t.Fatalf("runInitAll: %v", err)
	}
	for _, dir := range []string{baseRoot, baseServiceDir} {
		if _, err := os.Stat(filepath.Join(dir, claudeSettingsRel)); !os.IsNotExist(err) {
			t.Errorf("%s was created without --claude-hook (stat err = %v)", filepath.Join(dir, claudeSettingsRel), err)
		}
	}
}

// The other matcher tests compare primeHookMatchers against itself, so a matcher
// nobody thought of is invisible to them — which is how "fork" was missed. This
// one states Claude Code's documented set independently, so the code is checked
// against the contract rather than against its own opinion.
//
// From the SessionStart matcher table:
//
//	startup  new session
//	resume   --resume, --continue, or /resume
//	clear    /clear
//	compact  auto or manual compaction
//	fork     --fork-session with --resume or --continue, /fork, or /branch
//
// A matcher Claude Code does not define is never called, and one it defines that
// devstack omits is a session that starts with no briefing. Both fail silently,
// which is the only reason this is worth a test.
func TestBriefedMatchersAreEverySessionStartEvent(t *testing.T) {
	documented := []string{"startup", "resume", "clear", "compact", "fork"}

	have := map[string]bool{}
	for _, m := range primeHookMatchers {
		have[m] = true
	}
	for _, m := range documented {
		if !have[m] {
			t.Errorf("SessionStart fires %q and devstack does not brief it, so that session starts blind", m)
		}
		delete(have, m)
	}
	for m := range have {
		t.Errorf("devstack briefs %q, which SessionStart does not define — the hook would never run", m)
	}
}
