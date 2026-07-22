package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestStartCommandHasStackFlag(t *testing.T) {
	if svcStartCmd.Flags().Lookup("stack") == nil {
		t.Fatal("start command is missing the --stack flag")
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
