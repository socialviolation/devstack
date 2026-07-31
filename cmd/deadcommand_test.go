package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoDeadCommandReferences(t *testing.T) {
	// The verb-first forms were removed when the surface became noun first:
	// `devstack service start`, `devstack group stop`. A name can be both a
	// service and a group, and the old forms had to guess which was meant.
	dead := regexp.MustCompile(`devstack (up|generate|start|stop|restart|groups|deps)\b`)
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
					t.Errorf("%s:%d references a removed command (the surface is noun first: `devstack service start`, `devstack group stop`, `devstack dependencies list`, `devstack workspace up`): %s", path, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
