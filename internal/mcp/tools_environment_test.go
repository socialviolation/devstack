package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func writeStacksStore(t *testing.T, wsName, body string) {
	t.Helper()
	dir := workspace.DataDir(wsName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stacks.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestStacksSummaryListsInFlightStacks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeStacksStore(t, "navexa", `[
	  {"name":"perf","base":"navexa","root":"/x/perf","branch":"perf",
	   "overlay":["navexa-api"],"worktrees":{"navexa-api":"/x/perf/navexa-api"},
	   "ports":{"navexa-api/http":20000},"daemon_port":10351,"created_at":"2026-07-21T00:00:00Z"}
	]`)

	got := stacksSummary(&workspace.Workspace{Name: "navexa"})
	for _, want := range []string{"workspace: navexa", "in flight", "perf"} {
		if !strings.Contains(got, want) {
			t.Errorf("stacksSummary must surface %q so an agent sees other versions; got %q", want, got)
		}
	}
}

func TestStacksSummaryBaseOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := stacksSummary(&workspace.Workspace{Name: "navexa"})
	if !strings.Contains(got, "base only") {
		t.Errorf("a workspace with no stacks should say base only; got %q", got)
	}
}
