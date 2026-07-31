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

// registeredKinds maps a command path to the target kind it is pinned to. A
// pinned command rejects any other kind of name, so an example that names one is
// an invocation that cannot run.
func registeredKinds(root *cobra.Command) map[string]string {
	kinds := map[string]string{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			parent := ""
			if c != root {
				parent = c.Name() + " "
			}
			if kind := sub.Annotations["targetKind"]; kind != "" {
				for _, n := range append([]string{sub.Name()}, sub.Aliases...) {
					kinds[parent+n] = kind
				}
			}
			walk(sub)
		}
	}
	walk(root)
	return kinds
}

// commandExample matches a two-word command reference followed by a placeholder,
// e.g. `devstack service start <group>`.
var commandExample = regexp.MustCompile("devstack ([a-z][a-z-]*) ([a-z][a-z-]*) <([a-z][a-z-]*)>")

// A command pinned to one target kind rejects a name of any other kind, so help
// text that pairs it with the wrong placeholder names an invocation that cannot
// run. TestEveryReferencedCommandExists only asks whether the command path
// resolves, so `devstack service start <group>` passed it while the command
// itself refuses a group name.
func TestNoReferencedCommandContradictsItsTargetKind(t *testing.T) {
	kinds := registeredKinds(rootCmd)
	if len(kinds) == 0 {
		t.Fatal("no command declares a targetKind annotation — this guard checks nothing")
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
				for _, m := range commandExample.FindAllStringSubmatch(line, -1) {
					want, ok := kinds[m[1]+" "+m[2]]
					if !ok || m[3] == want {
						continue
					}
					if _, isKind := map[string]bool{"service": true, "group": true}[m[3]]; !isKind {
						continue
					}
					t.Errorf("%s:%d shows `devstack %s %s <%s>`, but that command only accepts a %s name: %s",
						path, i+1, m[1], m[2], m[3], want, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
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
