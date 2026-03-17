package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
)

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the Tilt UI in the browser",
	RunE:  runOpen,
}

func init() {
	rootCmd.AddCommand(openCmd)
	openCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from cwd)")
}

func runOpen(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")

	var ws *workspace.Workspace
	var err error

	if wsFlag != "" {
		ws, err = resolveWorkspace(wsFlag)
	} else {
		ws, err = workspace.DetectFromCwd()
	}
	if err != nil {
		return fmt.Errorf("could not resolve workspace: %w\nTry: devstack open --workspace=<name>", err)
	}

	url := fmt.Sprintf("http://localhost:%d", ws.TiltPort)
	fmt.Printf("Opening Tilt UI for '%s': %s\n", ws.Name, url)
	return exec.Command("xdg-open", url).Start()
}
