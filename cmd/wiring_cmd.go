package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
)

var wiringCmd = &cobra.Command{
	Use:   "wiring [service] [--stack <name>]",
	Short: "Show how each service reaches the services it talks to",
	Long: `Show how each service reaches the services it talks to.

For each edge A → B, devstack names the env key of A that carries the address
of B, and how it knows. It shows the value that key resolves to with no stack,
and the service that value reaches. With --stack, it also shows the value in
that stack.

With a service name, devstack shows only the edges into and out of it.
The report masks each value that holds a credential.

  devstack wiring
  devstack wiring api --stack feat`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runWiring,
}

func init() {
	rootCmd.AddCommand(wiringCmd)
	wiringCmd.Flags().String("stack", "", "Also show the value of each key in this stack")
}

func runWiring(cmd *cobra.Command, args []string) error {
	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	stackName, _ := cmd.Flags().GetString("stack")
	service := ""
	if len(args) > 0 {
		service = args[0]
	}
	out, err := nvxmcp.WiringReport(base, base.Path, stackName, service, true)
	if err != nil {
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), out)
	return nil
}
