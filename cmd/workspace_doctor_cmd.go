package cmd

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/workspace"
)

var workspaceDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check workspace manifests and topology integrity",
	RunE:  runWorkspaceDoctor,
}

func init() {
	workspaceCmd.AddCommand(workspaceDoctorCmd)
}

// reportGeneratedStaleness names the AGENTS.md files an older devstack wrote,
// and returns how many. It belongs here as well as in `upgrade` because the
// files go stale on their own schedule: a repo cloned today carries whatever was
// committed months ago, with no upgrade involved.
func reportGeneratedStaleness(wsPath string) int {
	files, err := scanGenerated(wsPath)
	if err != nil {
		return 0
	}
	stale := staleGenerated(files, buildStamp())
	if len(stale) == 0 {
		return 0
	}
	fmt.Printf("generated files: %d of %d written by an older devstack\n", len(stale), len(files))
	for _, f := range stale {
		fmt.Printf("- [warn] %s: AGENTS.md %s\n", f.Service, describeStamp(f.Version))
	}
	fmt.Println("  regenerate: devstack init --all")
	return len(stale)
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

	if composeSpec, err := infra.ResolveComposeSpec(ctx.WorkspaceRoot.Value); err == nil && composeSpec != nil {
		if running, err := infra.RunningServices(composeSpec); err == nil {
			fmt.Printf("infra: compose (%s)\n", strings.Join(running, ", "))
		} else {
			fmt.Printf("infra: compose status unavailable (%v)\n", err)
		}
	}

	drifted := reportConfigDrift(ctx.WorkspaceRoot.Value)
	outdated := reportGeneratedStaleness(ctx.WorkspaceRoot.Value)

	if len(graph.Issues) == 0 {
		if drifted == 0 && outdated == 0 {
			fmt.Println("status: ok")
		}
		return nil
	}

	fmt.Println("status: issues found")
	for _, issue := range graph.Issues {
		fmt.Printf("- [%s] %s\n", issue.Severity, issue.Message)
	}
	return fmt.Errorf("workspace doctor found %d issue(s)", len(graph.Issues))
}

// reportConfigDrift prints, for every service that declares config sources, the
// keys its deployment declares that the local env does not supply. It returns
// how many services drifted. Drift is reported but does not fail the doctor: a
// local stack is meant to differ from a deployment in places, and only the
// developer knows which.
func reportConfigDrift(workspacePath string) int {
	rw, err := config.ResolveWorkspace(workspacePath)
	if err != nil {
		return 0
	}
	ws, err := workspace.FindByPath(workspacePath)
	if err != nil {
		return 0
	}

	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	managed := workspace.ManagedEnv(ws, names, workspace.ActiveEnvNames(rw, ""))

	drifted := 0
	for _, name := range names {
		svc := rw.Services[name]
		if svc.Manifest == nil || len(svc.Manifest.Config.Sources) == 0 {
			continue
		}
		layers, err := config.EnvLadder(svc.EnvDir(), rw.Manifest, svc.Manifest, "", managed[name])
		if err != nil {
			continue
		}
		entries, err := svcconfig.Drift(svc, config.MergeEnvLadder(layers))
		if err != nil || len(entries) == 0 {
			continue
		}
		if drifted == 0 {
			fmt.Println("\nconfig drift (declared by the service's own config.sources, not supplied locally):")
		}
		drifted++
		fmt.Print(svcconfig.Render(name, entries))
	}
	return drifted
}
