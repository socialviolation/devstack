package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const humanAbove = "# api\n\nThis service owns billing. Read `docs/ledger.md` before you change a rate.\n\n" +
	"## House rules\n\n- Run `make lint` before every commit.\n- Never widen a decimal column.\n"

const humanBelow = "## BEADS\n\n- bead workflow notes a human wrote\n- and a second line, indented oddly\n     like this\n"

// The rule that matters most in this cleanup: devstack removes only what
// devstack wrote. A human's prose above and below the managed block comes out
// byte for byte the way it went in.
func TestCleanupLeavesHumanProseByteIdentical(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, "AGENTS.md")

	seed := humanAbove + "\n" +
		agentsSentinelBegin + "\nstale generated content\n" + agentsSentinelEnd + "\n\n" +
		agentsSentinelBegin + "\na duplicate an older devstack appended\n" + agentsSentinelEnd + "\n\n" +
		legacyAgentsHeader + "\n\nunsentinelled content from before the sentinels existed\n\n" +
		humanBelow
	writeFile(t, agents, seed)

	if err := writeAgentsMD("api", dir, "/home/dev/navexa", ""); err != nil {
		t.Fatalf("writeAgentsMD: %v", err)
	}
	got := readString(t, agents)

	if !strings.Contains(got, humanAbove) {
		t.Fatalf("the prose above the block was altered:\n%s", got)
	}
	if !strings.Contains(got, humanBelow) {
		t.Fatalf("the prose below the block was altered:\n%s", got)
	}
	if strings.Contains(got, "stale generated content") || strings.Contains(got, "a duplicate an older devstack") {
		t.Errorf("stale generated content survived:\n%s", got)
	}
	if strings.Contains(got, "unsentinelled content from before") {
		t.Errorf("the legacy unsentinelled section survived:\n%s", got)
	}
	if n := strings.Count(got, agentsSentinelBegin); n != 1 {
		t.Errorf("found %d managed blocks, want exactly 1", n)
	}
	if n := strings.Count(got, legacyAgentsHeader); n != 1 {
		t.Errorf("found %d devstack sections, want exactly 1 (the generated one)", n)
	}
}

// A second run must change nothing, or every refresh shows up as a diff.
func TestCleanupIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, "AGENTS.md")
	writeFile(t, agents, humanAbove+"\n"+
		agentsSentinelBegin+"\nstale\n"+agentsSentinelEnd+"\n\n"+
		agentsSentinelBegin+"\nduplicate\n"+agentsSentinelEnd+"\n\n"+humanBelow)

	if err := writeAgentsMD("api", dir, "/home/dev/navexa", ""); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := readString(t, agents)
	if err := writeAgentsMD("api", dir, "/home/dev/navexa", ""); err != nil {
		t.Fatalf("second: %v", err)
	}
	if second := readString(t, agents); first != second {
		t.Fatalf("not byte-identical on the second run:\n--- first ---\n%q\n--- second ---\n%q", first, second)
	}
}

// The pointer files belong to other tools, so the cleanup there is narrower: a
// duplicate managed block and an old unsentinelled devstack pointer go, and
// everything else stays.
func TestPointerCleanupDropsDuplicatesAndKeepsOtherTools(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, "CLAUDE.md")
	other := "## Some other tool\n\nIts own instructions, which devstack must not touch.\n"
	writeFile(t, claude, "# House rules\n\nAlways run the linter.\n\n"+
		agentsSentinelBegin+"\nstale pointer\n"+agentsSentinelEnd+"\n\n"+
		agentsSentinelBegin+"\nduplicate pointer\n"+agentsSentinelEnd+"\n\n"+
		legacyPointerHeader+"\n\nan old unsentinelled pointer block\n\n"+
		other)

	if _, err := writeAIInstructionPointers("api", dir, ""); err != nil {
		t.Fatalf("writeAIInstructionPointers: %v", err)
	}
	got := readString(t, claude)

	if !strings.Contains(got, "Always run the linter.") || !strings.Contains(got, other) {
		t.Fatalf("content that is not devstack's was altered:\n%s", got)
	}
	if strings.Contains(got, "duplicate pointer") || strings.Contains(got, "an old unsentinelled pointer block") {
		t.Errorf("stale devstack content survived:\n%s", got)
	}
	if n := strings.Count(got, agentsSentinelBegin); n != 1 {
		t.Errorf("found %d managed blocks, want exactly 1", n)
	}
	if n := strings.Count(got, legacyPointerHeader); n != 1 {
		t.Errorf("found %d devstack pointer sections, want exactly 1 (the generated one)", n)
	}
}

// The generated block must not carry a copy of what `devstack prime` prints at
// session start. A committed copy of a live fact goes stale, and a stale fact
// reads exactly like a true one.
func TestAgentInstructionsDoNotDuplicateTheLiveBriefing(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceManifest(t, root)
	block := buildAgentInstructions("api", filepath.Join(root, "api"), root, "")

	if len(block) > 12000 {
		t.Errorf("the generated block is %d chars; it should carry only what prime does not", len(block))
	}
	if !strings.Contains(block, "devstack prime") {
		t.Error("the block never sends the reader to the live briefing")
	}

	// The seven service states and the two safety rules are the parts prime does
	// not cover, so they have to survive the shrink.
	for _, want := range []string{
		"`running`", "`starting`", "`building`", "`stopped`", "`erroring`", "`disabled`", "`unknown`",
		"Never commit `devstack.service.yaml`",
		"If that manifest is committed, keep real secrets out of it",
		"`env.required`",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the shrink dropped %q, which prime does not cover", want)
		}
	}
}

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
