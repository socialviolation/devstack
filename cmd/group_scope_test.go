package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func groupCmdFor(t *testing.T, action string) *cobra.Command {
	t.Helper()
	return &cobra.Command{Use: action, Annotations: map[string]string{"targetKind": string(targetGroup)}}
}

// baseWithCore writes a base workspace declaring core with three members and
// returns it. The stack's own config is passed separately, because a stack's
// generated manifest lists only the members that made it into the overlay.
func baseWithCore(t *testing.T) *workspace.Workspace {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "dev", "navexa")
	for _, svc := range []string{"api", "frontend", "orbit"} {
		writeNested(t, filepath.Join(root, svc, config.ServiceManifestFileName), "version: 1\nservice:\n  name: "+svc+"\nruntime:\n  run:\n    command: go run .\n")
	}
	writeNested(t, filepath.Join(root, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: navexa
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
      - ./frontend
      - ./orbit
groups:
  core: [api, frontend, orbit]
`)
	return &workspace.Workspace{Name: "navexa", Path: root}
}

// A stack half-covering a group resolves to that half and says nothing, so
// "start core" reads as three services started when it started one.
func TestGroupActionInAStackNamesWhatStaysOnBase(t *testing.T) {
	ws := baseWithCore(t)
	stackCfg := &config.WorkspaceConfig{Groups: map[string][]string{"core": {"api"}}}

	out := captureStdout(t, func() {
		services, err := resolveInstanceTarget(groupCmdFor(t, "start"), ws, ws.Path, "core", stackCfg, "feat")
		if err != nil {
			t.Fatalf("resolveInstanceTarget: %v", err)
		}
		if strings.Join(services, ",") != "api" {
			t.Errorf("services = %v, want the stack's half of the group", services)
		}
	})

	for _, want := range []string{"1 of 3", "feat", "frontend", "orbit", "base"} {
		if !strings.Contains(out, want) {
			t.Errorf("shortfall notice must mention %q, got: %s", want, out)
		}
	}
}

// The whole group in the stack is the unremarkable case and must stay quiet.
func TestGroupActionSaysNothingWhenTheStackCoversTheGroup(t *testing.T) {
	ws := baseWithCore(t)
	stackCfg := &config.WorkspaceConfig{Groups: map[string][]string{"core": {"api", "frontend", "orbit"}}}

	out := captureStdout(t, func() {
		if _, err := resolveInstanceTarget(groupCmdFor(t, "start"), ws, ws.Path, "core", stackCfg, "feat"); err != nil {
			t.Fatalf("resolveInstanceTarget: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("a fully covered group must print nothing, got: %s", out)
	}
}

// A group no member of which is in the stack resolves as "not a known group",
// while devstack group list — which reads base — shows it. Two answers for one
// name, and neither says why.
func TestGroupMissingEntirelyFromAStackExplainsWhy(t *testing.T) {
	ws := baseWithCore(t)
	stackCfg := &config.WorkspaceConfig{Groups: map[string][]string{}}

	_, err := resolveInstanceTarget(groupCmdFor(t, "start"), ws, ws.Path, "core", stackCfg, "feat")
	if err == nil {
		t.Fatal("expected an error for a group with no services in the stack")
	}
	for _, want := range []string{"core", "feat", "runs entirely on base", "--stack base"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q, got: %v", want, err)
		}
	}
}

// Base has no such group either: the plain "not a known group" answer is the
// right one, and must not be dressed up as a stack shortfall.
func TestUnknownGroupStaysUnknown(t *testing.T) {
	ws := baseWithCore(t)
	stackCfg := &config.WorkspaceConfig{Groups: map[string][]string{}}

	_, err := resolveInstanceTarget(groupCmdFor(t, "start"), ws, ws.Path, "nope", stackCfg, "feat")
	if err == nil {
		t.Fatal("expected an error for a group nothing declares")
	}
	if strings.Contains(err.Error(), "runs entirely on base") {
		t.Errorf("a group base does not declare must not be reported as a shortfall: %v", err)
	}
}

// Base itself is not a stack, so there is no shortfall to report there.
func TestGroupActionOnBaseIsUnchanged(t *testing.T) {
	ws := baseWithCore(t)
	cfg, err := config.Load(ws.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	out := captureStdout(t, func() {
		services, err := resolveInstanceTarget(groupCmdFor(t, "start"), ws, ws.Path, "core", cfg, "")
		if err != nil {
			t.Fatalf("resolveInstanceTarget: %v", err)
		}
		if len(services) != 3 {
			t.Errorf("services = %v, want all three of base's copies", services)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("base must print no shortfall notice, got: %s", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig, origColor := os.Stdout, color.Output
	os.Stdout, color.Output = w, w
	defer func() { os.Stdout, color.Output = orig, origColor }()

	fn()
	w.Close()
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	return string(buf[:n])
}
