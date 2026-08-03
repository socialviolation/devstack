package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/stack"
)

func stackConfigCmdFor(t *testing.T, stackName string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "config"}
	c.Flags().String("stack", "", "")
	if err := c.Flags().Set("stack", stackName); err != nil {
		t.Fatalf("set --stack: %v", err)
	}
	return c
}

// stack config makes no daemon call, so it can not report what runs. It reads
// the stack's files. For a stack that is down it must say the stack is down,
// and it must not describe the table as the configuration in use.
func TestStackConfigSaysAStackIsDown(t *testing.T) {
	rec, _ := buildStackScenario(t)
	viper.Set("workspace", "navexa")
	t.Cleanup(func() { viper.Set("workspace", "") })

	out := captureStdout(t, func() {
		if err := runStackConfig(stackConfigCmdFor(t, rec.Name), []string{"backend"}); err != nil {
			t.Fatalf("runStackConfig: %v", err)
		}
	})
	for _, want := range []string{"is down", "would run with", "devstack stack up feat"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "the configuration it runs with") {
		t.Errorf("a down stack must not be described as running:\n%s", out)
	}
}

// An up stack keeps the plain wording, so the two states read differently.
func TestStackConfigSaysAnUpStackRuns(t *testing.T) {
	rec, _ := buildStackScenario(t)
	viper.Set("workspace", "navexa")
	t.Cleanup(func() { viper.Set("workspace", "") })
	if err := stack.SetActive("navexa", rec.Name, true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runStackConfig(stackConfigCmdFor(t, rec.Name), []string{"backend"}); err != nil {
			t.Fatalf("runStackConfig: %v", err)
		}
	})
	if !strings.Contains(out, "the configuration it runs with") {
		t.Errorf("an up stack must keep the plain wording, got:\n%s", out)
	}
	if strings.Contains(out, "is down") {
		t.Errorf("an up stack must not be reported as down:\n%s", out)
	}
}
