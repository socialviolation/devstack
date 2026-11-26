package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func seedTaskWiringWorkspace(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	mk := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, content)
	}
	api := "version: 1\nservice:\n  name: api\nruntime:\n  run:\n    command: go run .\nports:\n  http: 5001\ntasks:\n  hello:\n    run: echo hello-from-task\n  fail:\n    run: exit 3\n"
	web := "version: 1\nservice:\n  name: web\nruntime:\n  run:\n    command: npm start\nenv:\n  values:\n    API_URL: ${api.url}\n"

	root := filepath.Join(home, "dev", "shop")
	mk(filepath.Join(root, "api", config.ServiceManifestFileName), api)
	mk(filepath.Join(root, "web", config.ServiceManifestFileName), web)
	mk(filepath.Join(root, config.WorkspaceManifestFileName), "version: 2\nworkspace:\n  name: shop\n  repoDiscovery:\n    mode: explicit\n    repos: [./api, ./web]\n")

	stackRoot := filepath.Join(home, "stacks", "feat")
	worktree := filepath.Join(home, "wt", "api")
	mk(filepath.Join(stackRoot, config.WorkspaceManifestFileName), "version: 2\nworkspace:\n  name: shop--feat\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - "+worktree+"\n")
	mk(filepath.Join(worktree, config.ServiceManifestFileName), api)

	data, err := json.Marshal([]stack.Record{{
		Name: "feat", Base: "shop", Root: stackRoot,
		Members: []string{"api"}, Worktrees: map[string]string{"api": worktree},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mk(stack.StoreFile("shop"), string(data))
	if err := workspace.Register(workspace.Workspace{Name: "shop", Path: root}); err != nil {
		t.Fatal(err)
	}
	useWorkspaceKey(t, "shop")
	return worktree
}

func stackFlagCommand(stackName string) (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{Use: "test"}
	c.Flags().String("stack", stackName, "")
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetContext(context.Background())
	return c, &buf
}

func TestTaskRunRunsTheDeclaredTaskInTheStack(t *testing.T) {
	seedTaskWiringWorkspace(t)
	c, out := stackFlagCommand("feat")
	if err := runTaskRun(c, []string{"api", "hello"}); err != nil {
		t.Fatalf("task run: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "exited 0") || !strings.Contains(out.String(), "hello-from-task") {
		t.Fatalf("task run output:\n%s", out)
	}
}

func TestTaskRunFailsWhenTheTaskFails(t *testing.T) {
	seedTaskWiringWorkspace(t)
	c, out := stackFlagCommand("feat")
	if err := runTaskRun(c, []string{"api", "fail"}); err == nil {
		t.Fatalf("a task that exits 3 returned no error:\n%s", out)
	}
	if !strings.Contains(out.String(), "exited 3") {
		t.Fatalf("task run output:\n%s", out)
	}
}

func TestTaskRunListsTheTasksWithNoTaskName(t *testing.T) {
	seedTaskWiringWorkspace(t)
	c, out := stackFlagCommand("feat")
	if err := runTaskRun(c, []string{"api"}); err != nil {
		t.Fatalf("task run: %v", err)
	}
	if !strings.Contains(out.String(), "hello") || !strings.Contains(out.String(), "fail") {
		t.Fatalf("task list output:\n%s", out)
	}
}

func TestWiringShowsEachEdge(t *testing.T) {
	seedTaskWiringWorkspace(t)
	c, out := stackFlagCommand("feat")
	if err := runWiring(c, nil); err != nil {
		t.Fatalf("wiring: %v", err)
	}
	for _, want := range []string{"web → api", "API_URL", "in stack feat"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("wiring output omits %q:\n%s", want, out)
		}
	}
}

func TestTaskRunAndWiringAreRegistered(t *testing.T) {
	for _, path := range [][]string{{"task", "run"}, {"wiring"}} {
		c, _, err := rootCmd.Find(path)
		if err != nil || c.Name() != path[len(path)-1] {
			t.Errorf("devstack %s is not registered", strings.Join(path, " "))
		}
	}
	for _, name := range []string{"task", "wiring"} {
		if !strings.Contains(moreScreen(), "devstack "+name+" ") {
			t.Errorf("devstack help more does not list %s:\n%s", name, moreScreen())
		}
	}
}
