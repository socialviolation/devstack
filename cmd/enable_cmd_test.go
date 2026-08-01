package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every service and group action can target a stack's copy instead of base.
func TestNounCommandsHaveStackFlag(t *testing.T) {
	for _, c := range []*cobra.Command{
		serviceStartCmd, serviceStopCmd, serviceRestartCmd,
		groupStartCmd, groupStopCmd, groupRestartCmd,
	} {
		if c.Flags().Lookup("stack") == nil {
			t.Errorf("%q is missing the --stack flag", c.CommandPath())
		}
	}
}

func newEnableTestCmd(workspace, stackName string) *cobra.Command {
	c := &cobra.Command{Use: "start"}
	c.Flags().String("workspace", workspace, "")
	c.Flags().String("group", "", "")
	c.Flags().String("stack", stackName, "")
	return c
}

// runEnable must route --stack through resolveStackTarget: an inactive stack is
// "not up", so runEnable errors before touching Tilt. Dropping the resolveStackTarget
// call (reverting to base) would skip this guard and change the error.
func TestRunEnableStackConsultsResolveStackTarget(t *testing.T) {
	buildStackScenario(t)

	err := runEnable(newEnableTestCmd("navexa", "feat"), nil)
	if err == nil {
		t.Fatal("expected an error for a stack that is not up")
	}
	if !strings.Contains(err.Error(), "not up") || !strings.Contains(err.Error(), "devstack stack up feat") {
		t.Errorf("expected resolveStackTarget's 'not up' guidance, got: %v", err)
	}
}
