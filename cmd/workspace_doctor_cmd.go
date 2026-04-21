package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"devstack/internal/config"
)

var workspaceDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check workspace manifests and topology integrity",
	RunE:  runWorkspaceDoctor,
}

func init() {
	workspaceCmd.AddCommand(workspaceDoctorCmd)
}

func runWorkspaceDoctor(cmd *cobra.Command, args []string) error {
	ctx, err := resolveExplainContext(cmd)
	if err != nil {
		return err
	}
	graph, err := config.BuildTopology(ctx.WorkspaceRoot.Value)
	if err != nil {
		return err
	}

	manifestFile := filepath.Join(graph.WorkspaceRoot, config.WorkspaceManifestFileName)
	if ctx.Workspace != nil && ctx.Workspace.Source == ".devstack.json" {
		manifestFile = filepath.Join(graph.WorkspaceRoot, ".devstack.json")
	}

	fmt.Printf("Workspace doctor: %s\n", graph.WorkspaceName)
	fmt.Printf("root: %s\n", graph.WorkspaceRoot)
	fmt.Printf("config: %s\n", manifestFile)
	fmt.Printf("services: %d\n", len(graph.Services))

	if len(graph.Issues) == 0 {
		fmt.Println("status: ok")
		return nil
	}

	fmt.Println("status: issues found")
	for _, issue := range graph.Issues {
		fmt.Printf("- [%s] %s\n", issue.Severity, issue.Message)
	}
	return fmt.Errorf("workspace doctor found %d issue(s)", len(graph.Issues))
}
