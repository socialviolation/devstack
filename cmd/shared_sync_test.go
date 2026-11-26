package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func serviceManifest(name string) string {
	return "version: 1\nservice:\n  name: " + name + "\nruntime:\n  run:\n    command: go run .\n"
}

func gitRepoWith(t *testing.T, dir string, services map[string]string) {
	t.Helper()
	for sub, name := range services {
		writeAt(t, filepath.Join(dir, sub, config.ServiceManifestFileName), serviceManifest(name))
	}
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main", "-q")
	run("config", "commit.gpgsign", "false")
	run("add", "-f", ".")
	run("commit", "-q", "-m", "init")
}

func workspaceAt(t *testing.T, root, name string, repos ...string) *workspace.Workspace {
	t.Helper()
	manifest := "version: 1\nworkspace:\n  name: " + name + "\n  env: dev\n  repoDiscovery:\n    mode: explicit\n    repos:\n"
	for _, r := range repos {
		manifest += "      - ./" + r + "\n"
	}
	writeAt(t, filepath.Join(root, config.WorkspaceManifestFileName), manifest)
	return &workspace.Workspace{Name: name, Path: root}
}

func shareServices(t *testing.T, root string, names ...string) {
	t.Helper()
	path := filepath.Join(root, config.WorkspaceManifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeAt(t, path, string(data)+"shared: ["+strings.Join(names, ", ")+"]\n")
}

func TestSharedSyncBuildsTheReplicaThatIsNotThereAndStartsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	shareServices(t, root, "api")
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	useWorkspaceKey(t, ws.Name)

	if err := runSharedSync(nil, nil); err != nil {
		t.Fatalf("runSharedSync() with no replica = %v", err)
	}

	if !config.HasWorkspaceManifest(replica.Root(ws)) {
		t.Fatalf("no replica manifest at %s", replica.Root(ws))
	}
	if _, err := os.Stat(filepath.Join(replica.Root(ws), "api", config.ServiceManifestFileName)); err != nil {
		t.Errorf("the replica holds no worktree for api: %v", err)
	}

	for _, path := range []string{workspace.HostTiltDir(), workspace.HostPIDFile()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("base sync started the daemon: %s exists", path)
		}
	}

	if err := runSharedSync(nil, nil); err != nil {
		t.Fatalf("runSharedSync() on a built replica = %v", err)
	}
}
