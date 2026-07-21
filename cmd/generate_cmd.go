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
		if _, err := stack.Resolve(base.Name, stackName); err != nil {
			return err
		}
		return generateBase(base)
	}
	if base, _, err := stack.DetectFromCwd(); err == nil {
		return generateBase(base)
	}

	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	if !config.HasWorkspaceManifest(ws.Path) {
		return fmt.Errorf("no %s in %s — this workspace isn't manifest-based yet", config.WorkspaceManifestFileName, ws.Path)
	}
	return generateBase(ws)
}

func generateBase(ws *workspace.Workspace) error {
	path, err := regenerateTiltfile(ws)
	if err != nil {
		return err
	}
	fmt.Printf("✓ Generated %s\n", path)
	return nil
}

// regenerateTiltfile renders a base workspace's Tiltfile from its manifests plus
// every active feature stack's overlay services (namespaced <svc>:<stack>) and
// writes it to <ws.Path>/Tiltfile. With no active stacks the output is
// byte-identical to base-only generation. Returns the written path.
func regenerateTiltfile(ws *workspace.Workspace) (string, error) {
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace manifests: %w", err)
	}

	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}

	stackGens, err := activeStackGens(ws)
	if err != nil {
		return "", err
	}

	out, err := tiltgen.GenerateCombined(rw, tiltgen.Options{ManagedEnv: workspace.ManagedEnv(ws, names)}, stackGens)
	if err != nil {
		return "", err
	}

	path := filepath.Join(ws.Path, "Tiltfile")
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return "", fmt.Errorf("failed to write Tiltfile: %w", err)
	}
	return path, nil
}

// activeStackGens builds a tiltgen.StackGen for every active feature stack of the
// base workspace: its resolved worktree checkout, its overlay-first options, and
// its short name as the namespace. Returns nil when no stack is active.
func activeStackGens(ws *workspace.Workspace) ([]tiltgen.StackGen, error) {
	recs, err := stack.LoadStore(ws.Name)
	if err != nil {
		return nil, err
	}
	var gens []tiltgen.StackGen
	for i := range recs {
		rec := recs[i]
		if !rec.Active {
			continue
		}
		rw, err := config.ResolveWorkspace(rec.Root)
		if err != nil {
			return nil, fmt.Errorf("stack %q: failed to resolve manifests: %w", rec.Name, err)
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		opts, err := stack.GenerateOptions(&rec, names)
		if err != nil {
			return nil, fmt.Errorf("stack %q: %w", rec.Name, err)
		}
		gens = append(gens, tiltgen.StackGen{Workspace: rw, Options: opts, Namespace: rec.Name})
	}
	return gens, nil
}
