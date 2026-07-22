package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoDeadCommandReferences(t *testing.T) {
	dead := regexp.MustCompile(`devstack (up|generate)\b`)
	roots := []string{".", filepath.Join("..", "internal")}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(data), "\n") {
				if dead.MatchString(line) {
					t.Errorf("%s:%d references the non-existent command (use `devstack workspace up`/`workspace generate`): %s", path, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
