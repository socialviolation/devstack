package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
)

var topologyCmd = &cobra.Command{
	Use:   "topology [service]",
	Short: "Explain the workspace, or one service's resolved configuration",
	Long: `With no arguments, devstack shows the resolved workspace and environment. It
also shows the service graph: the groups, the dependencies and the dependents.

With a service name, devstack shows that service's fully resolved configuration.
It also shows where each value comes from.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTopology,
}

func init() {
	workspaceCmd.AddCommand(topologyCmd)
}

func runTopology(cmd *cobra.Command, args []string) error {
	ctx, err := resolveExplainContext(cmd)
	if err != nil {
		return err
	}

	// With a service argument, explain just that service.
	if len(args) > 0 {
		return printServiceExplain(ctx, args[0])
	}

	// Otherwise: workspace/environment resolution + the service graph.
	printConfigExplain(ctx)
	fmt.Println()

	graph, err := config.BuildTopology(ctx.WorkspaceRoot.Value)
	if err != nil {
		return err
	}

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
