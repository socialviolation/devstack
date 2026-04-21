package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"devstack/internal/telemetry"
)

var telemetryCmd = &cobra.Command{
	Use:   "telemetry",
	Short: "Inspect telemetry evidence and confidence",
}

var telemetryStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show telemetry evidence and confidence for services in a workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, err := resolveExplainContext(cmd)
		if err != nil {
			return err
		}
		statuses, err := telemetry.Status(ctx.WorkspaceRoot.Value)
		if err != nil {
			return err
		}
		fmt.Printf("Workspace: %s\n", ctx.WorkspaceName.Value)
		for _, status := range statuses {
			fmt.Printf("- %s: confidence=%s traces_expected=%t logs_expected=%t collector_reachable=%t traces=%d logs=%t mode=%s\n", status.Service, status.Confidence, status.ExpectedTraces, status.ExpectedLogs, status.CollectorReachable, status.TraceCount, status.LogEvidence, status.Mode)
			fmt.Printf("  %s\n", status.Interpretation)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(telemetryCmd)
	telemetryCmd.AddCommand(telemetryStatusCmd)
}
