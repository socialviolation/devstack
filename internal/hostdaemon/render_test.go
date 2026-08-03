package hostdaemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

func rungit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func makeRepo(t *testing.T, dir, service string, port int) {
	t.Helper()
	manifest := "version: 1\nservice:\n  name: " + service + "\nruntime:\n  run:\n    command: go run .\nports:\n  http: " + strconv.Itoa(port) + "\n"
	writeFile(t, filepath.Join(dir, "devstack.service.yaml"), manifest)
	rungit(t, dir, "init", "-b", "main", "-q")
	rungit(t, dir, "config", "commit.gpgsign", "false")
	rungit(t, dir, "add", "-f", ".")
	rungit(t, dir, "commit", "-q", "-m", "init")
}

func newActiveWorkspace(t *testing.T, home, name, service string, port int) *workspace.Workspace {
	t.Helper()
	root := filepath.Join(home, "dev", name)
	makeRepo(t, filepath.Join(root, service), service, port)
	writeFile(t, filepath.Join(root, "devstack.workspace.yaml"),
		"version: 1\nworkspace:\n  name: "+name+"\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./"+service+"\n")

	if err := workspace.Register(workspace.Workspace{Name: name, Path: root, TiltPort: port}); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	if err := workspace.SetWorkspaceActive(name, true); err != nil {
		t.Fatalf("activate %s: %v", name, err)
	}
	return &workspace.Workspace{Name: name, Path: root}
}

func serveDir(t *testing.T, tiltfile, resource string) string {
	t.Helper()
	block, ok := resourceBlocks(tiltfile)[resource]
	if !ok {
		t.Fatalf("resource %q missing from the generated Tiltfile:\n%s", resource, tiltfile)
	}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if rest, found := strings.CutPrefix(line, "serve_dir="); found {
			return strings.Trim(strings.TrimSuffix(rest, ","), `"`)
		}
	}
	t.Fatalf("resource %q has no serve_dir:\n%s", resource, block)
	return ""
}

func TestRenderRunsFromTheReplica(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := newActiveWorkspace(t, home, "navexa", "backend", 8080)
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	out, warnings, err := render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none when every workspace has a replica", warnings)
	}
	want := filepath.Join(replica.Root(ws), "backend")
	if got := serveDir(t, out, "navexa:backend"); got != want {
		t.Errorf("serve_dir = %q, want the replica worktree %q", got, want)
	}
}

func TestRenderFallsBackToTheCheckoutAndWarns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := newActiveWorkspace(t, home, "navexa", "backend", 8080)

	out, warnings, err := render()
	if err != nil {
		t.Fatalf("render must not fail for a workspace with no replica: %v", err)
	}
	want := filepath.Join(ws.Path, "backend")
	if got := serveDir(t, out, "navexa:backend"); got != want {
		t.Errorf("serve_dir = %q, want the checkout %q", got, want)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "navexa") || !strings.Contains(warnings[0], "devstack workspace up") {
		t.Errorf("warning = %q, want it to name the workspace and what to run", warnings[0])
	}
}

// One Tiltfile serves the whole machine, so a workspace nobody has built a
// replica for must not take the others' generation down with it.
func TestRenderUnbuiltWorkspaceDoesNotBreakABuiltOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	built := newActiveWorkspace(t, home, "navexa", "backend", 8080)
	unbuilt := newActiveWorkspace(t, home, "southfoundry", "api", 8090)
	if _, err := replica.Ensure(built); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	out, warnings, err := render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if got, want := serveDir(t, out, "navexa:backend"), filepath.Join(replica.Root(built), "backend"); got != want {
		t.Errorf("built workspace serve_dir = %q, want the replica worktree %q", got, want)
	}
	if got, want := serveDir(t, out, "southfoundry:api"), filepath.Join(unbuilt.Path, "api"); got != want {
		t.Errorf("unbuilt workspace serve_dir = %q, want the checkout %q", got, want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "southfoundry") {
		t.Errorf("warnings = %v, want one naming only southfoundry", warnings)
	}
}

func TestRenderBrokenManifestStaysAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := newActiveWorkspace(t, home, "navexa", "backend", 8080)
	writeFile(t, filepath.Join(ws.Path, "devstack.workspace.yaml"), "version: 1\nworkspace: [not a mapping\n")

	if _, _, err := render(); err == nil {
		t.Fatal("render = nil error for a workspace whose manifest does not parse")
	}
}
