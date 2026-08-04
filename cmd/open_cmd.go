package cmd

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/workspace"
)

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the dev daemon dashboard in the browser",
	Long: `Open the dev daemon UI of the current workspace in the browser.
The dashboard shows every running service, its build logs and its state.`,
	RunE: runOpen,
}

func init() {
	workspaceCmd.AddCommand(openCmd)
}

func runOpen(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace") // inherited persistent flag

	var ws *workspace.Workspace
	var err error

	ws, err = resolveWorkspace(wsFlag)
	if err != nil {
		return fmt.Errorf("can not resolve the workspace: %w\nTry: devstack workspace open --workspace=<name>", err)
	}

	url := fmt.Sprintf("http://localhost:%d", workspace.HostTiltPort)
	fmt.Printf("Opening dashboard for '%s': %s\n", ws.Name, url)
	return exec.Command("xdg-open", url).Start()
}
