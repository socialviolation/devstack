package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// claudeSettingsRel is the file Claude Code reads for a project's settings.
const claudeSettingsRel = ".claude/settings.json"

// primeHookCommand is the command the SessionStart hook runs. It is matched by
// substring, so a user who adds flags of their own keeps one entry.
const primeHookCommand = "devstack prime --json"

// primeHookMatchers are the SessionStart events worth briefing. It is every
// matcher Claude Code defines, because each one begins a session whose context
// does not carry the briefing forward.
//
// "compact" matters most after the first: compaction is exactly the moment the
// landscape falls out of context, and an agent that forgets there are several
// copies of a service starts reporting the wrong one as broken. "fork" is the
// same event wearing another name — /fork and /branch start a session against a
// workspace whose state has moved on since the parent was briefed.
//
// Adding one here is enough to patch every settings.json already written:
// mergePrimeHook adds the matchers that are missing and leaves the rest alone.
var primeHookMatchers = []string{"startup", "resume", "clear", "compact", "fork"}

// ensureClaudeSessionHook merges devstack's SessionStart hook into dir's
// .claude/settings.json and reports whether it changed the file.
//
// It merges rather than overwrites: every other key survives, and so does every
// hook the user already declared. An entry that already runs `devstack prime` is
// left alone, so running init twice adds nothing the second time.
func ensureClaudeSessionHook(dir string) (bool, error) {
	path := filepath.Join(dir, claudeSettingsRel)

	settings := map[string]any{}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if uerr := json.Unmarshal(existing, &settings); uerr != nil {
			return false, fmt.Errorf("%s is not valid JSON, so devstack did not touch it: %w", path, uerr)
		}
	case !os.IsNotExist(err):
		return false, fmt.Errorf("failed to read %s: %w", path, err)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	sessionStart, _ := hooks["SessionStart"].([]any)

	updated, changed := mergePrimeHook(sessionStart)
	if !changed {
		return false, nil
	}

	hooks["SessionStart"] = updated
	settings["hooks"] = hooks

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return false, fmt.Errorf("failed to write %s: %w", path, err)
	}
	return true, nil
}

// mergePrimeHook adds the prime hook to each matcher that does not run it yet,
// and returns the SessionStart list with a flag saying whether anything changed.
func mergePrimeHook(sessionStart []any) ([]any, bool) {
	changed := false
	for _, matcher := range primeHookMatchers {
		entry := findMatcherEntry(sessionStart, matcher)
		if entry == nil {
			sessionStart = append(sessionStart, map[string]any{
				"matcher": matcher,
				"hooks":   []any{primeHookEntry()},
			})
			changed = true
			continue
		}
		inner, _ := entry["hooks"].([]any)
		if hasPrimeHook(inner) {
			continue
		}
		entry["hooks"] = append(inner, primeHookEntry())
		changed = true
	}
	return sessionStart, changed
}

func primeHookEntry() map[string]any {
	return map[string]any{"type": "command", "command": primeHookCommand}
}

func findMatcherEntry(sessionStart []any, matcher string) map[string]any {
	for _, raw := range sessionStart {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if s, _ := entry["matcher"].(string); s == matcher {
			return entry
		}
	}
	return nil
}

// hasPrimeHook reports whether a matcher's hook list already runs devstack
// prime, so a second init adds no duplicate.
func hasPrimeHook(inner []any) bool {
	for _, raw := range inner {
		h, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := h["command"].(string); isPrimeCommand(cmd) {
			return true
		}
	}
	return false
}

// isPrimeCommand matches any invocation of `devstack prime`, whatever flags the
// user added, so devstack never appends a near-duplicate of their own entry.
func isPrimeCommand(cmd string) bool {
	return strings.Contains(cmd, "devstack prime")
}
