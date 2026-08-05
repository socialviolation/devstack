package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func newEnvTestCmd(stackName, service string) *cobra.Command {
	c := &cobra.Command{Use: "env"}
	c.Flags().String("stack", stackName, "")
	c.Flags().String("service", service, "")
	c.Flags().Bool("shadowed", false, "")
	c.Flags().Bool("reveal", false, "")
	return c
}

// noTargetRefusal reports whether an error is the resolver refusing for want of
// a named copy, rather than any of the failures a command can still hit without
// a daemon. The refusal is gone, so no mutating verb may produce it.
func noTargetRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no copy named")
}

// The template checkout names no copy, so a restart typed there means base. It
// used to refuse and make the caller type `--stack base`, which was the only
// copy it could have meant.
func TestServiceRestartInTheCheckoutUsesBase(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	t.Chdir(base.Path)

	if err := runRestart(newEnableTestCmd("navexa", ""), []string{"backend"}); noTargetRefusal(err) {
		t.Errorf("a restart in the checkout must resolve to base, got: %v", err)
	}
}

// Same for stopping and starting, so no mutating verb keeps the old refusal.
func TestServiceStartAndStopUseBaseInTheCheckout(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	t.Chdir(base.Path)

	for name, run := range map[string]func(*cobra.Command, []string) error{
		"start": runEnable,
		"stop":  runStop,
	} {
		if err := run(newEnableTestCmd("navexa", ""), []string{"backend"}); noTargetRefusal(err) {
			t.Errorf("%s in the checkout must resolve to base, got: %v", name, err)
		}
	}
}

// env use points a scope at an environment. With no --stack that scope is base,
// and with --service it is that service — neither needs the caller to say so.
func TestEnvUseDefaultsToBase(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	if err := config.SetEnvValue(base.Path, "dev", "LOG_LEVEL", "debug"); err != nil {
		t.Fatalf("define env: %v", err)
	}
	t.Chdir(base.Path)

	if err := runEnvUse(newEnvTestCmd("", ""), []string{"dev"}); err != nil {
		t.Errorf("env use with no --stack must apply to base: %v", err)
	}
	if err := runEnvUse(newEnvTestCmd("", "backend"), []string{"dev"}); err != nil {
		t.Errorf("env use --service names its own scope and must still work: %v", err)
	}
}

// env set defines an environment in the workspace manifest, which every stack
// inherits. There is no instance to pick, so the target rule does not apply to
// it — requiring one would be friction with nothing behind it, and would refuse
// the command outright inside a stack worktree.
func TestEnvSetNeedsNoTarget(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	t.Chdir(base.Path)

	if err := runEnvSet(newEnvTestCmd("", ""), []string{"dev", "LOG_LEVEL=debug"}); err != nil {
		t.Errorf("env set must work with no target, got: %v", err)
	}
}

// The regression this rule most easily causes: a read-only command has no
// instance to change, so it must still answer with no --stack and no directory
// hint, standing in the plain checkout.
func TestReadOnlyEnvWhichStillWorksWithNoTarget(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	t.Chdir(base.Path)

	if err := runEnvWhich(newEnvTestCmd("", "backend"), nil); err != nil {
		t.Errorf("env which must not demand a target: %v", err)
	}
}
