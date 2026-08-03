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

// The template checkout is not an implicit base: a command that restarts a
// service refuses there and says what to type, instead of restarting base's
// replica — which is not the code the caller is standing in.
func TestServiceRestartInTheCheckoutRefusesWithoutATarget(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	t.Chdir(base.Path)

	err = runRestart(newEnableTestCmd("navexa", ""), []string{"backend"})
	if err == nil {
		t.Fatal("expected a restart with no --stack in the checkout to refuse")
	}
	for _, want := range []string{"--stack base", "feat"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q, got: %v", want, err)
		}
	}
}

// Same rule for stopping and starting, so no mutating verb keeps the old silent
// default.
func TestServiceStartAndStopRefuseWithoutATarget(t *testing.T) {
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
		if err := run(newEnableTestCmd("navexa", ""), []string{"backend"}); err == nil {
			t.Errorf("%s with no --stack in the checkout must refuse", name)
		} else if !strings.Contains(err.Error(), "--stack base") {
			t.Errorf("%s refusal must name --stack base, got: %v", name, err)
		}
	}
}

// env use points a scope at an environment, so it needs the same explicit
// instance — with --service it does not, because that names a scope of its own.
func TestEnvUseNeedsATargetUnlessAServiceIsNamed(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	if err := config.SetEnvValue(base.Path, "dev", "LOG_LEVEL", "debug"); err != nil {
		t.Fatalf("define env: %v", err)
	}
	t.Chdir(base.Path)

	err = runEnvUse(newEnvTestCmd("", ""), []string{"dev"})
	if err == nil {
		t.Fatal("expected env use with no --stack in the checkout to refuse")
	}
	if !strings.Contains(err.Error(), "--stack base") {
		t.Errorf("refusal must name --stack base, got: %v", err)
	}

	if err := runEnvUse(newEnvTestCmd("", "backend"), []string{"dev"}); err != nil {
		t.Errorf("env use --service names its own scope and must still work: %v", err)
	}
}

// env set writes environments, which are defined once in base and inherited, so
// it demands the same explicit target and can only accept base.
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
