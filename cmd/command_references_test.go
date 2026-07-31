package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// commandRef matches a `devstack <word> [<word>]` invocation. It requires the
// reference to be delimited — a backtick, a quote, or the start of a line in a
// usage block — so prose that merely uses "devstack" as a noun ("devstack runs
// your services") is not mistaken for a command.
var commandRef = regexp.MustCompile("(?m)(?:[`'\"]|^\\s*|: )devstack ([a-z][a-z-]*)(?: ([a-z][a-z-]*))?")

// registeredPaths returns every command path that actually exists, as "noun" and
// "noun action".
func registeredPaths(root *cobra.Command) map[string]bool {
	paths := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			names := append([]string{sub.Name()}, sub.Aliases...)
			parent := ""
			if c != root {
				parent = c.Name() + " "
			}
			for _, n := range names {
				paths[parent+n] = true
			}
			walk(sub)
		}
	}
	walk(root)
	return paths
}

// A command named in help text, an error message or the generated AGENTS.md must
// exist. Telling someone to run `devstack services` when there is no such
// command sends them in a circle, and a denylist of removed names cannot catch
// it — this checks the opposite direction, that everything referenced resolves.
func TestEveryReferencedCommandExists(t *testing.T) {
	paths := registeredPaths(rootCmd)

	// A line of prose can begin with "devstack", so the English verbs that
	// follow it there are excluded by name. The guard is for command
	// references; it does not try to parse sentences.
	notCommands := map[string]bool{
		"": true, "help": true,
		"is": true, "runs": true, "manages": true, "inspects": true,
		"will": true, "capabilities": true, "tunnels": true, "sets": true,
		"resolves": true, "reads": true, "writes": true, "detects": true, "maintains": true,
	}

	roots := []string{".", filepath.Join("..", "internal")}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for i, line := range strings.Split(string(data), "\n") {
				for _, m := range commandRef.FindAllStringSubmatch(line, -1) {
					noun, action := m[1], m[2]
					if notCommands[noun] {
						continue
					}
					if paths[noun+" "+action] || paths[noun] {
						continue
					}
					t.Errorf("%s:%d names `devstack %s`, which is not a registered command: %s",
						path, i+1, strings.TrimSpace(noun+" "+action), strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
