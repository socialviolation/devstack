package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAServiceThatIsNotSharedNeedsTheStackFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	gitRepoWith(t, filepath.Join(root, "db"), map[string]string{".": "db"})
	ws := workspaceAt(t, root, "shop", "api", "db")
	shareServices(t, root, "db")

	if err := requireSharedWithoutStack(ws, "", []string{"db"}); err != nil {
		t.Errorf("a shared service with no stack = %v, want nil", err)
	}
	err := requireSharedWithoutStack(ws, "", []string{"api"})
	if err == nil || !strings.Contains(err.Error(), "--stack") || !strings.Contains(err.Error(), `"api"`) {
		t.Errorf("a stack service with no stack = %v, want an error that names api and --stack", err)
	}
	if err := requireSharedWithoutStack(ws, "feat", []string{"api"}); err != nil {
		t.Errorf("a stack service in a stack = %v, want nil", err)
	}
}
