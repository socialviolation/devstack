package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
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
	workspaceGenerateCmd.Flags().String("stack", "", "Generate for a named feature stack of the resolved workspace instead of the base")
}

func runGenerate(cmd *cobra.Command, args []string) error {
	stackName, _ := cmd.Flags().GetString("stack")
	if stackName != "" {
		base, err := resolveWorkspace(viper.GetString("workspace"))
		if err != nil {
			return err
		}
		rec, err := stack.Resolve(base.Name, stackName)
		if err != nil {
			return err
		}
		return generateStack(rec)
	}
	if base, rec, err := stack.DetectFromCwd(); err == nil {
		_ = base
		return generateStack(rec)
	}

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

func generateStack(rec *stack.Record) error {
	path, err := regenerateStackTiltfile(rec)
	if err != nil {
		return err
	}
	fmt.Printf("✓ Generated %s\n", path)
	return nil
}

// regenerateTiltfile renders a base workspace's Tiltfile from its manifests and
// writes it to <ws.Path>/Tiltfile. Returns the written path.
func regenerateTiltfile(ws *workspace.Workspace) (string, error) {
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace manifests: %w", err)
	}

	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}

	out, err := tiltgen.Generate(rw, tiltgen.Options{ManagedEnv: workspace.ManagedEnv(ws, names)})
	if err != nil {
		return "", err
	}

	path := filepath.Join(ws.Path, "Tiltfile")
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return "", fmt.Errorf("failed to write Tiltfile: %w", err)
	}
	return path, nil
}

// regenerateStackTiltfile renders a feature stack's Tiltfile from its generated
// manifest at the stack root, using the overlay-first port book (base's pinned
// ports with the stack's allocated ports over its overlay services) and pointing
// OTEL at the base's collector. Written to <stack root>/Tiltfile.
func regenerateStackTiltfile(rec *stack.Record) (string, error) {
	rw, err := config.ResolveWorkspace(rec.Root)
	if err != nil {
		return "", fmt.Errorf("failed to resolve stack manifests: %w", err)
	}

	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}

	opts, err := stack.GenerateOptions(rec, names)
	if err != nil {
		return "", err
	}

	out, err := tiltgen.Generate(rw, opts)
	if err != nil {
		return "", err
	}

	path := filepath.Join(rec.Root, "Tiltfile")
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return "", fmt.Errorf("failed to write Tiltfile: %w", err)
	}
	return path, nil
}
