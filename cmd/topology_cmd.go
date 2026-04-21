package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
)

var topologyCmd = &cobra.Command{
	Use:   "topology",
	Short: "Show services, groups, dependencies, and dependents",
	RunE:  runTopology,
}

func init() {
	rootCmd.AddCommand(topologyCmd)
}

func runTopology(cmd *cobra.Command, args []string) error {
	ctx, err := resolveExplainContext(cmd)
	if err != nil {
		return err
	}
	graph, err := config.BuildTopology(ctx.WorkspaceRoot.Value)
	if err != nil {
		return err
	}

	fmt.Printf("Workspace: %s\n", graph.WorkspaceName)
	fmt.Printf("Root: %s\n", graph.WorkspaceRoot)
	fmt.Println()

	if len(graph.Groups) == 0 {
		fmt.Println("Groups: -")
	} else {
		fmt.Println("Groups:")
		for _, group := range graph.GroupNames() {
			fmt.Printf("  - %s: %s\n", group, strings.Join(graph.Groups[group], ", "))
		}
	}
	fmt.Println()

	fmt.Println("Services:")
	for _, name := range graph.ServiceNames() {
		service := graph.Services[name]
		fmt.Printf("  - %s\n", service.Name)
		fmt.Printf("      path: %s\n", service.Path)
		fmt.Printf("      groups: %s\n", printableCSV(service.Groups))
		fmt.Printf("      dependencies: %s\n", printableCSV(service.Dependencies))
		fmt.Printf("      dependents: %s\n", printableCSV(service.Dependents))
	}

	if len(graph.Issues) > 0 {
		fmt.Println()
		fmt.Println("Issues:")
		for _, issue := range graph.Issues {
			fmt.Printf("  - [%s] %s\n", issue.Severity, issue.Message)
		}
	}

	return nil
}

func printableCSV(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}
