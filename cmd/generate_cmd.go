package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

// workspaceGenerateCmd manually refreshes the host daemon's Tiltfile. It normally
// runs automatically as part of `devstack workspace up`, so this is only for
// inspecting the artifact without starting the daemon.
var workspaceGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Regenerate the host daemon's Tiltfile from devstack manifests",
	Long: `Regenerate the host daemon's Tiltfile — the single file the running Tilt
daemon reads — composing every active workspace's base services plus each
active feature stack's overlay services.

The Tiltfile is a build artifact — edit the manifests, not the Tiltfile.
Runs automatically as part of 'devstack workspace up'.`,
	SilenceUsage: true,
	RunE:         runGenerate,
}

func init() {
	workspaceCmd.AddCommand(workspaceGenerateCmd)
}

func runGenerate(cmd *cobra.Command, args []string) error {
	path, err := regenerateHostTiltfile()
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

// regenerateHostTiltfile renders the one host Tiltfile composing every active
// workspace's base services plus each active stack's overlay services, all as
// distinct resources prefixed <ws>:<svc>, and writes it to the host Tilt dir. A
// running host daemon hot-reloads it. With no active workspaces it still writes a
// valid header-only Tiltfile so a running daemon drains to empty. Returns the path.
func regenerateHostTiltfile() (string, error) {
	active, err := workspace.ActiveWorkspaces()
	if err != nil {
		return "", err
	}

	var gens []tiltgen.WorkspaceGen
	for i := range active {
		ws := active[i]
		rw, err := config.ResolveWorkspace(ws.Path)
		if err != nil {
			return "", fmt.Errorf("workspace %q: failed to resolve manifests: %w", ws.Name, err)
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		stackGens, err := activeStackGens(&ws)
		if err != nil {
			return "", err
		}
		gens = append(gens, tiltgen.WorkspaceGen{
			Name:     ws.Name,
			Base:     rw,
			BaseOpts: tiltgen.Options{ManagedEnv: workspace.ManagedEnv(&ws, names)},
			Stacks:   stackGens,
		})
	}

	out, err := tiltgen.GenerateHost(gens)
	if err != nil {
		return "", err
	}

	dir := workspace.HostTiltDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create host tilt dir: %w", err)
	}
	path := filepath.Join(dir, "Tiltfile")
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return "", fmt.Errorf("failed to write host Tiltfile: %w", err)
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
