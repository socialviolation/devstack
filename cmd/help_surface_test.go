package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// rootHelp renders the screen that `devstack --help` prints.
func rootHelp(t *testing.T) string {
	t.Helper()
	installHelp()
	var b bytes.Buffer
	rootCmd.SetOut(&b)
	t.Cleanup(func() { rootCmd.SetOut(nil) })
	if err := rootCmd.Help(); err != nil {
		t.Fatalf("root help: %v", err)
	}
	return b.String()
}

// The first screen is the only documentation most people read. An alphabetic
// list of 18 nouns says what devstack holds, and never what to do with it. These
// are the steps of one feature, in the order that somebody does them.
func TestTheFirstScreenTeachesTheFeatureFlow(t *testing.T) {
	help := rootHelp(t)

	for _, want := range []string{
		"WORK ON A FEATURE",
		"devstack stack create",
		"devstack stack up",
		"devstack status",
		"devstack service restart",
		"devstack stack note",
		"devstack stack rm",
		"POINT IT SOMEWHERE",
		"devstack env use",
		"SET UP THIS MACHINE",
		"devstack workspace add",
		"devstack workspace up",
		"devstack upgrade",
		"devstack help more",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("the first screen never states %q:\n%s", want, help)
		}
	}

	if strings.Index(help, "WORK ON A FEATURE") > strings.Index(help, "SET UP THIS MACHINE") {
		t.Error("the first screen sets the machine up before it works a feature")
	}
}

// A command that no human types is noise on a screen that teaches. Each one
// still works, and each one keeps its own help.
func TestTheFirstScreenNamesNoHiddenCommand(t *testing.T) {
	help := rootHelp(t)

	hidden := 0
	for _, c := range rootCmd.Commands() {
		if !c.Hidden {
			continue
		}
		hidden++
		if strings.Contains(help, "devstack "+c.Name()) {
			t.Errorf("the first screen names the hidden command %q:\n%s", c.Name(), help)
		}
	}
	if hidden < 4 {
		t.Fatalf("only %d commands are hidden, so this guard proves nothing", hidden)
	}
}

// The demoted commands are real, and somebody needs each one now and then. They
// leave the first screen, and they lose nothing else.
func TestHelpMoreNamesEveryDemotedCommand(t *testing.T) {
	installHelp()
	screen := moreScreen()

	for _, name := range []string{"otel", "tunnel", "ports", "dependencies", "hooks", "group"} {
		if !strings.Contains(screen, "devstack "+name) {
			t.Errorf("`devstack help more` never names %q:\n%s", name, screen)
		}
		if leafCommand(name) == nil {
			t.Errorf("`devstack %s` is not registered, and `help more` sends the reader to it", name)
		}
	}
	if !strings.Contains(screen, "works as it did before") {
		t.Errorf("`devstack help more` never says that these commands are unchanged:\n%s", screen)
	}
}

// The workflow screen belongs to the root command alone. cobra reads the help
// template from the parent when a command has none, so a hidden command showed
// the root's screen in place of its own.
func TestEveryHiddenCommandKeepsItsOwnHelp(t *testing.T) {
	installHelp()

	for _, name := range []string{"base", "prime", "serve", "migrate", "completion"} {
		c := leafCommand(name)
		if c == nil {
			t.Fatalf("`devstack %s` is not registered", name)
		}
		if !c.Hidden {
			t.Errorf("`devstack %s` is back on the first screen", name)
		}

		var b bytes.Buffer
		c.SetOut(&b)
		if err := c.Help(); err != nil {
			t.Fatalf("`devstack %s --help`: %v", name, err)
		}
		got := b.String()
		if !strings.Contains(got, "Usage:") {
			t.Errorf("`devstack %s --help` prints no usage:\n%s", name, got)
		}
		if strings.Contains(got, "WORK ON A FEATURE") {
			t.Errorf("`devstack %s --help` prints the root's screen:\n%s", name, got)
		}
	}
}
