package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/workspace"
)

func fakeTiltLogs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\nprintf 'one\\ntwo\\nthree\\nfour\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "tilt"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func serviceLogsTestCmd(t *testing.T, flags ...string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "logs"}
	c.Flags().String("stack", "", "")
	c.Flags().Int("lines", 100, "")
	if err := c.Flags().Parse(flags); err != nil {
		t.Fatal(err)
	}
	return c
}

func logsWorkspace(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	gitRepoWith(t, filepath.Join(root, "db"), map[string]string{".": "db"})
	ws := workspaceAt(t, root, "shop", "api", "db")
	shareServices(t, root, "db")
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	useWorkspaceKey(t, ws.Name)
}

func TestServiceLogsPrintsTheLastLinesOfASharedService(t *testing.T) {
	logsWorkspace(t)
	serveDaemon(t, `{"uiResources":[{"metadata":{"name":"shop:db"}}]}`)
	argsFile := fakeTiltLogs(t)

	c := serviceLogsTestCmd(t, "--lines", "2")
	var out strings.Builder
	c.SetOut(&out)
	if err := runServiceLogs(c, []string{"db"}); err != nil {
		t.Fatalf("service logs db: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "three\nfour") || strings.Contains(got, "two") {
		t.Errorf("output = %q, want only the last 2 lines", got)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("tilt did not run: %v", err)
	}
	if !strings.HasPrefix(string(args), "logs shop:db ") {
		t.Errorf("tilt args = %q, want logs of shop:db", args)
	}
}

func TestServiceLogsAsksForAStackForAServiceThatIsNotShared(t *testing.T) {
	logsWorkspace(t)
	serveDaemon(t, `{"uiResources":[{"metadata":{"name":"shop:db"}}]}`)
	fakeTiltLogs(t)

	err := runServiceLogs(serviceLogsTestCmd(t), []string{"api"})
	if err == nil || !strings.Contains(err.Error(), "stack") || !strings.Contains(err.Error(), `"api"`) {
		t.Errorf("service logs api with no stack = %v, want an error that names api and a stack", err)
	}
}
