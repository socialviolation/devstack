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

func notSharedRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not a shared service")
}

func TestServiceCommandsInTheCheckoutResolveOnlyASharedService(t *testing.T) {
	buildStackScenario(t)
	base, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("find base: %v", err)
	}
	t.Chdir(base.Path)

	for name, run := range map[string]func(*cobra.Command, []string) error{
		"start":   runEnable,
		"stop":    runStop,
		"restart": runRestart,
	} {
		if err := run(newEnableTestCmd("navexa", ""), []string{"db"}); notSharedRefusal(err) {
			t.Errorf("%s db in the checkout must resolve to the shared service, got: %v", name, err)
		}
		if err := run(newEnableTestCmd("navexa", ""), []string{"backend"}); !notSharedRefusal(err) {
			t.Errorf("%s backend in the checkout must ask for --stack, got: %v", name, err)
		}
	}
}

func TestEnvUseDefaultsToTheWorkspace(t *testing.T) {
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
		t.Errorf("env use with no --stack must apply to the workspace: %v", err)
	}
	if err := runEnvUse(newEnvTestCmd("", "backend"), []string{"dev"}); err != nil {
		t.Errorf("env use --service names its own scope and must still work: %v", err)
	}
}

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
