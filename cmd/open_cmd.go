package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
)

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the dev daemon dashboard in the browser",
	Long: `Open the dev daemon UI for the current workspace in the browser.
The dashboard shows all running services, their build logs, and status.`,
	RunE:  runOpen,
}

func init() {
	workspaceCmd.AddCommand(openCmd)
}

func runOpen(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace") // inherited persistent flag

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
	fmt.Printf("Opening dashboard for '%s': %s\n", ws.Name, url)
	return exec.Command("xdg-open", url).Start()
}
