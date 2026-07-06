package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

// workspaceGenerateCmd manually refreshes the generated Tiltfile. It normally
// runs automatically as part of `devstack workspace up`, so this is only for
// inspecting the artifact without starting the daemon.
var workspaceGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate the dev daemon config from devstack manifests",
	Long: `Regenerate the workspace's Tiltfile from its devstack manifests
(devstack.workspace.yaml + each service's devstack.service.yaml).

The Tiltfile is a build artifact — edit the manifests, not the Tiltfile.
Runs automatically as part of 'devstack workspace up'.`,
	SilenceUsage: true,
	RunE:         runGenerate,
}

func init() {
	workspaceCmd.AddCommand(workspaceGenerateCmd)
}

func runGenerate(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	if !config.HasWorkspaceManifest(ws.Path) {
		return fmt.Errorf("no %s in %s — this workspace isn't manifest-based yet", config.WorkspaceManifestFileName, ws.Path)
	}
	path, err := regenerateTiltfile(ws)
	if err != nil {
		return err
	}
	fmt.Printf("✓ Generated %s\n", path)
	return nil
}

// regenerateTiltfile renders the workspace's Tiltfile from its manifests and
// writes it to <ws.Path>/Tiltfile. Returns the written path.
func regenerateTiltfile(ws *workspace.Workspace) (string, error) {
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace manifests: %w", err)
	}

	managed := map[string]string{}
	// Only push OTEL export env down to services when the workspace opts into
	// observability. Otherwise services are left un-instrumented by default.
	if config.ObservabilityEnabled(ws.Path) {
		if ep := workspace.OtelOTLPEndpoint(ws); ep != "" {
			managed["OTEL_EXPORTER_OTLP_ENDPOINT"] = ep
			managed["OTEL_EXPORTER_OTLP_PROTOCOL"] = "grpc"
		}
	}

	out, err := tiltgen.Generate(rw, tiltgen.Options{ManagedEnv: managed})
	if err != nil {
		return "", err
	}

	path := filepath.Join(ws.Path, "Tiltfile")
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return "", fmt.Errorf("failed to write Tiltfile: %w", err)
	}
	return path, nil
}
