package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"devstack/internal/workspace"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage devstack environments",
	Long: `List, add, and remove named environments for your workspace.

Environments let you point devstack at different infrastructure (local dev, staging, prod).
Local environments have full service control. Remote environments are observability-only.`,
}

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List environments for the current workspace",
	RunE:  runEnvList,
}

var envAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add or update a named environment",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnvAdd,
}

var envRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a named environment",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnvRemove,
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.AddCommand(envListCmd, envAddCmd, envRemoveCmd)

	envAddCmd.Flags().String("type", "remote", `environment type: "local" or "remote"`)
	envAddCmd.Flags().String("backend", "signoz", `observability backend (currently only "signoz")`)
	envAddCmd.Flags().String("url", "", "observability backend URL (required)")
	envAddCmd.Flags().String("otlp-endpoint", "", "OTLP ingestion URL for the collector (e.g. https://otel.company.com:4318)")
	envAddCmd.Flags().String("api-key", "", "API key for remote observability backend")
	_ = envAddCmd.MarkFlagRequired("url")
}

func runEnvList(cmd *cobra.Command, args []string) error {
	wsName := viper.GetString("workspace")
	ws, err := resolveEnvWorkspace(wsName)
	if err != nil {
		return err
	}

	allEnvs := ws.AllEnvironments()
	activeEnvName := viper.GetString("environment")
	if activeEnvName == "" {
		activeEnvName = "local"
	}

	// Sort env names for deterministic output
	names := make([]string, 0, len(allEnvs))
	for k := range allEnvs {
		names = append(names, k)
	}
	sort.Strings(names)

	fmt.Printf("Environments for workspace %q:\n\n", ws.Name)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tBACKEND\tURL\tOTLP ENDPOINT\t")
	fmt.Fprintln(w, "----\t----\t-------\t---\t-------------\t")
	for _, name := range names {
		env := allEnvs[name]
		marker := ""
		if name == activeEnvName {
			marker = " <- active"
		}
		backend := env.Observability.Backend
		if backend == "" {
			backend = "signoz"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s%s\n", name, env.Type, backend, env.Observability.URL, env.Observability.OTLPEndpoint, marker)
	}
	w.Flush()

	fmt.Printf("\nTo switch: set DEVSTACK_ENVIRONMENT=<name> or use --env=<name>\n")
	return nil
}

func runEnvAdd(cmd *cobra.Command, args []string) error {
	envName := args[0]

	wsName := viper.GetString("workspace")
	ws, err := resolveEnvWorkspace(wsName)
	if err != nil {
		return err
	}

	envType, _ := cmd.Flags().GetString("type")
	backend, _ := cmd.Flags().GetString("backend")
	url, _ := cmd.Flags().GetString("url")
	otlpEndpoint, _ := cmd.Flags().GetString("otlp-endpoint")
	apiKey, _ := cmd.Flags().GetString("api-key")

	var eType workspace.EnvironmentType
	switch envType {
	case "local":
		eType = workspace.EnvironmentTypeLocal
	case "remote":
		eType = workspace.EnvironmentTypeRemote
	default:
		return fmt.Errorf("invalid type %q: must be \"local\" or \"remote\"", envType)
	}

	env := workspace.Environment{
		Type: eType,
		Observability: workspace.ObservabilityConfig{
			Backend:      backend,
			URL:          url,
			OTLPEndpoint: otlpEndpoint,
			APIKey:       apiKey,
		},
	}

	if err := workspace.AddEnvironment(ws.Name, envName, env); err != nil {
		return fmt.Errorf("failed to add environment: %w", err)
	}

	fmt.Printf("Added environment %q to workspace %q\n", envName, ws.Name)
	fmt.Printf("  Type:    %s\n", eType)
	fmt.Printf("  Backend: %s\n", backend)
	fmt.Printf("  URL:     %s\n", url)
	if otlpEndpoint != "" {
		fmt.Printf("  OTLP:    %s\n", otlpEndpoint)
	}
	if apiKey != "" {
		fmt.Printf("  API Key: (set)\n")
	}
	return nil
}

func runEnvRemove(cmd *cobra.Command, args []string) error {
	envName := args[0]

	wsName := viper.GetString("workspace")
	ws, err := resolveEnvWorkspace(wsName)
	if err != nil {
		return err
	}

	if err := workspace.RemoveEnvironment(ws.Name, envName); err != nil {
		return fmt.Errorf("failed to remove environment: %w", err)
	}

	fmt.Printf("Removed environment %q from workspace %q\n", envName, ws.Name)
	return nil
}

// resolveEnvWorkspace finds the workspace to operate on for env commands.
// Uses --workspace flag, DEVSTACK_WORKSPACE env var, or detects from cwd.
func resolveEnvWorkspace(wsName string) (*workspace.Workspace, error) {
	if wsName != "" {
		ws, err := workspace.FindByName(wsName)
		if err != nil {
			return nil, fmt.Errorf("workspace %q not found: %w", wsName, err)
		}
		return ws, nil
	}
	ws, err := workspace.DetectFromCwd()
	if err != nil {
		return nil, fmt.Errorf("could not detect workspace from current directory. Use --workspace or DEVSTACK_WORKSPACE: %w", err)
	}
	return ws, nil
}
