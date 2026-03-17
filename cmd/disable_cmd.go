package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"devstack/internal/tilt"
)

var disableCmd = &cobra.Command{
	Use:   "disable <service>",
	Short: "Disable a service in the workspace",
	Long: `Disable a service by running tilt disable for it.

Note: dependencies are NOT disabled — they may be shared by other services.
Disable each service explicitly.`,
	Args: cobra.ExactArgs(1),
	RunE: runDisable,
}

func init() {
	rootCmd.AddCommand(disableCmd)
	disableCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
}

func runDisable(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	service := args[0]

	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	out, err := tiltClient.RunCLI("disable", service)
	if err != nil {
		if out != "" {
			fmt.Print(out)
		}
		return fmt.Errorf("tilt disable failed: %w", err)
	}

	if out != "" {
		fmt.Print(out)
	}
	fmt.Printf("✓ Disabled: %s\n", service)

	return nil
}
