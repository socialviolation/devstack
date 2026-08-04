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

	// A line of prose can begin with "devstack", so the English words that follow
	// it there are excluded by name. The guard is for command references; it does
	// not try to parse sentences.
	//
	// The list is long because CLAUDE.md Rule 5 requires the active voice with the
	// actor named — "devstack builds the replica", not "the replica is built" — so
	// most user-facing prose about devstack now puts a verb straight after the
	// name. Every entry here is a word that is not, and could not be, a command:
	// no devstack command is a third-person verb, a modal or an adverb. An
	// invented noun such as `devstack services` is still caught, which is the
	// direction this guard exists for. Add a word here only after checking that
	// no command could ever carry that name.
	//
	// "help" is deliberately absent. root.go replaces cobra's help command with
	// a hidden one, so `devstack help <command>` does not resolve — excluding it
	// is what let the session briefing send every agent to a command that does
	// not exist. The working form is `devstack <command> --help`.
	notCommands := map[string]bool{
		"": true,
		// modals, adverbs and connectives
		"also": true, "can": true, "never": true, "then": true, "will": true,
		// simple-past and past-participle forms
		"cut": true, "did": true, "found": true, "regenerated": true, "resolved": true,
		"saw": true, "skipped": true, "stored": true, "turned": true,
		// third-person present verbs
		"allocates": true, "builds": true, "checks": true, "cuts": true,
		"defines": true, "derives": true, "detects": true, "does": true,
		"fires": true, "has": true, "inspects": true, "installs": true,
		"is": true, "keeps": true, "leaves": true, "looks": true,
		"maintains": true, "manages": true, "merges": true, "opens": true,
		"prints": true, "reads": true, "redacts": true, "refreshes": true,
		"registers": true, "remembers": true, "removes": true, "resolves": true,
		"runs": true, "sets": true, "starts": true, "stops": true,
		"stores": true, "terminates": true, "upgrades": true, "writes": true,
		// plural nouns that are not command names
		"capabilities": true, "tunnels": true,
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
