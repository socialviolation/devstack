package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// leafCommand returns the command registered at the given path, or nil when the
// last element is not a subcommand of the one before it.
func leafCommand(path ...string) *cobra.Command {
	found, _, err := rootCmd.Find(path)
	if err != nil || found.Name() != path[len(path)-1] {
		return nil
	}
	return found
}

func TestNoDeadCommandReferences(t *testing.T) {
	// The verb-first forms were removed when the surface became noun first:
	// `devstack service start`, `devstack group stop`. A name can be both a
	// service and a group, and the old forms had to guess which was meant.
	//
	// The rest were removed because their names hid what they touched:
	// `otel enable/disable/configure` read as siblings of `otel start/stop` while
	// one pair writes config and the other runs a process (now `otel config
	// on|off|set`); `tunnel list` sat beside `tunnel status` answering a
	// different question in nearly the same words (now `tunnel status
	// --planned`); and `topology` describes the workspace, so it moved under it.
	dead := regexp.MustCompile(`devstack (up|generate|start|stop|restart|groups|deps|topology|otel (enable|disable|configure)|tunnel list)\b`)
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

// `otel enable` and `otel start` read as the same kind of thing, and a user who
// wanted a collector running got a config write that started nothing (or the
// reverse: a collector that vanishes on the next `workspace up`). Config lives
// under `otel config`, the process under `otel start`/`stop`, and each says so.
func TestOtelSeparatesConfigFromProcess(t *testing.T) {
	for _, gone := range [][]string{{"otel", "enable"}, {"otel", "disable"}, {"otel", "configure"}} {
		if leafCommand(gone...) != nil {
			t.Errorf("`devstack %s` is back; it does not say whether it writes config or runs a process", strings.Join(gone, " "))
		}
	}
	for _, want := range [][]string{{"otel", "config", "on"}, {"otel", "config", "off"}, {"otel", "config", "set"}} {
		c := leafCommand(want...)
		if c == nil {
			t.Fatalf("`devstack %s` is not registered", strings.Join(want, " "))
		}
		if !strings.Contains(c.Short, "config") && !strings.Contains(c.Short, "manifest") {
			t.Errorf("`%s` short help must say it writes configuration: %q", c.CommandPath(), c.Short)
		}
	}
	for _, name := range []string{"start", "stop"} {
		c := leafCommand("otel", name)
		if c == nil {
			t.Fatalf("`devstack otel %s` is not registered", name)
		}
		if !strings.Contains(c.Short, "no configuration") {
			t.Errorf("`%s` short help must say it changes no configuration: %q", c.CommandPath(), c.Short)
		}
	}
}

// `tunnel list` (what would be forwarded) and `tunnel status` (what is
// forwarded) answered different questions in nearly the same words, and the
// wrong one reads as an answer. The preview is a flag on status now, and it
// needs the flags that shape what a forward would cover.
func TestTunnelPreviewIsAFlagOnStatus(t *testing.T) {
	if leafCommand("tunnel", "list") != nil {
		t.Error("`devstack tunnel list` is back beside `tunnel status`")
	}
	status := leafCommand("tunnel", "status")
	if status == nil {
		t.Fatal("`devstack tunnel status` is not registered")
	}
	for _, flag := range []string{"planned", "service", "stacks", "as-base", "otel"} {
		if status.Flags().Lookup(flag) == nil {
			t.Errorf("`tunnel status` cannot preview without --%s", flag)
		}
	}
}

// Every other stack operation is a noun — `stack up`, `stack down`, `stack rm` —
// so a status you could only reach through a flag on a different command was the
// odd one out. The workspace-level `status --stack` stays: that command is about
// the workspace and the flag narrows it.
func TestStackStatusIsReachableAsANoun(t *testing.T) {
	if leafCommand("stack", "status") == nil {
		t.Error("`devstack stack status <name>` is not registered")
	}
	if statusCmd.Flags().Lookup("stack") == nil {
		t.Error("`devstack status --stack <name>` was removed; it is legitimate on a workspace-level command")
	}
}

// topology describes the workspace, so it belongs to it.
func TestTopologyLivesUnderWorkspace(t *testing.T) {
	if leafCommand("workspace", "topology") == nil {
		t.Error("`devstack workspace topology` is not registered")
	}
	if leafCommand("topology") != nil {
		t.Error("`devstack topology` is back at the top level")
	}
}
