package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

// eventConstant maps an event name back to the Go constant a firing point
// spells it with, since call sites use the constant, never the literal.
var eventConstant = map[string]string{
	config.EventWorkspaceUp:   "config.EventWorkspaceUp",
	config.EventWorkspaceDown: "config.EventWorkspaceDown",
	config.EventStackCreate:   "config.EventStackCreate",
	config.EventStackUp:       "config.EventStackUp",
	config.EventStackDown:     "config.EventStackDown",
	config.EventStackDestroy:  "config.EventStackDestroy",
	config.EventServiceStart:  "config.EventServiceStart",
	config.EventServiceStop:   "config.EventServiceStop",
}

// An event devstack advertises in `hooks list` but never fires is worse than no
// event: you write a hook against it and it silently never runs.
func TestEveryDeclaredEventHasAFiringPoint(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd dir: %v", err)
	}

	var sources strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "hooks_cmd.go" {
			continue // the hooks command itself resolves events, it does not fire them
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sources.Write(data)
	}
	body := sources.String()

	for _, event := range config.HookEvents() {
		constant, ok := eventConstant[event]
		if !ok {
			t.Fatalf("event %q has no entry in eventConstant — add it alongside the new event", event)
		}
		if !strings.Contains(body, "fireHooks(") || !strings.Contains(body, constant) {
			t.Errorf("event %q (%s) is declared but never fired by any command", event, constant)
		}
	}
}
