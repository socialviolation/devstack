package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/observability"
	"github.com/socialviolation/devstack/internal/workspace"
)

// useWorkspaceKey points the viper "workspace" key at one value, and clears the
// override again when the test ends. A nil value clears it. An override that
// outlives its test hides DEVSTACK_WORKSPACE from every test that follows.
func useWorkspaceKey(t *testing.T, value any) {
	t.Helper()
	viper.Set("workspace", value)
	t.Cleanup(func() { viper.Set("workspace", nil) })
}

func otelSubcommand(t *testing.T, workspaceFlag string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "status"}
	c.Flags().String("workspace", "", "")
	if workspaceFlag != "" {
		if err := c.Flags().Set("workspace", workspaceFlag); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// Every otel subcommand declares its own --workspace flag, which shadows the
// persistent flag of the root command. viper binds that persistent flag, and
// DEVSTACK_WORKSPACE with it. So a subcommand that reads its local flag alone
// ignores the environment variable that its own help promises.
func TestOtelSubcommandsHonourTheWorkspaceEnvVar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := workspace.Register(workspace.Workspace{Name: "beta", Path: t.TempDir(), TiltPort: 10351}); err != nil {
		t.Fatalf("register beta: %v", err)
	}
	t.Chdir(t.TempDir())

	// The bindings are installed by cobra's OnInitialize, which no unit test
	// goes through.
	initConfig()
	useWorkspaceKey(t, nil)
	t.Setenv("DEVSTACK_WORKSPACE", "beta")

	ws, err := resolveOtelWorkspace(otelSubcommand(t, ""))
	if err != nil {
		t.Fatalf("resolveOtelWorkspace: %v", err)
	}
	if ws.Name != "beta" {
		t.Errorf("workspace = %q, want beta from DEVSTACK_WORKSPACE", ws.Name)
	}
}

// The local flag still wins over the environment variable.
func TestOtelSubcommandFlagBeatsTheEnvVar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, ws := range []workspace.Workspace{
		{Name: "alpha", Path: t.TempDir(), TiltPort: 10350},
		{Name: "beta", Path: t.TempDir(), TiltPort: 10351},
	} {
		if err := workspace.Register(ws); err != nil {
			t.Fatalf("register %s: %v", ws.Name, err)
		}
	}
	t.Chdir(t.TempDir())

	initConfig()
	useWorkspaceKey(t, nil)
	t.Setenv("DEVSTACK_WORKSPACE", "beta")

	ws, err := resolveOtelWorkspace(otelSubcommand(t, "alpha"))
	if err != nil {
		t.Fatalf("resolveOtelWorkspace: %v", err)
	}
	if ws.Name != "alpha" {
		t.Errorf("workspace = %q, want the flag's alpha", ws.Name)
	}
}

// `otel traces` and the investigate MCP tool are the same capability with the
// same word. An unqualified query used to cover every copy here and base only
// there, so a human and an agent comparing notes disagreed about what they had
// searched. Both surfaces now quote one sentence, and base only is the narrower
// reading of the two.
func TestOtelTracesDefaultsToBaseAlone(t *testing.T) {
	usage := otelTracesCmd.Flags().Lookup("stack").Usage
	if !strings.Contains(usage, observability.StackScopeDesc) {
		t.Errorf("--stack must state the shared default %q; got %q", observability.StackScopeDesc, usage)
	}
	if !strings.Contains(otelTracesCmd.Long, "base alone") || !strings.Contains(otelTracesCmd.Long, "--stack all") {
		t.Errorf("the command help must state the default and how to widen it:\n%s", otelTracesCmd.Long)
	}

	cases := map[string]string{"": "base", "  ": "base", "all": "", "*": ""}
	for in, want := range cases {
		got, err := resolveStackFlag(otelSubcommand(t, ""), in)
		if err != nil {
			t.Fatalf("resolveStackFlag(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("resolveStackFlag(%q) = %q, want %q", in, got, want)
		}
	}
}
