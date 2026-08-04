package cmd

import (
	"fmt"
	"io"
	"os"
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
	Short: "Examine the workspace manifests and the topology for problems",
	RunE:  runWorkspaceDoctor,
}

func init() {
	workspaceCmd.AddCommand(workspaceDoctorCmd)
}

// reportDevstackResidue names the files that still hold instructions devstack
// wrote, and returns how many. It belongs here as well as in `upgrade` because a
// repository carries whatever was committed months ago, with no upgrade
// involved.
func reportDevstackResidue(w io.Writer, wsPath string) int {
	ws, err := workspace.FindByPath(wsPath)
	if err != nil {
		return 0
	}
	files := workspaceResidue(ws)
	if len(files) == 0 {
		return 0
	}
	fmt.Fprintf(w, "devstack instructions: a block that devstack wrote is still in %s\n", pluralFiles(len(files)))
	for _, f := range files {
		note := ""
		if f.NeedsHuman {
			note = " (a marker has no pair, so a human must remove that block)"
		}
		fmt.Fprintf(w, "- [warn] %s%s\n", f.Path, note)
	}
	fmt.Fprintln(w, "  To read what devstack removes here, and change nothing, run: devstack migrate --list")
	fmt.Fprintln(w, "  Then remove it: devstack migrate")
	return len(files)
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
	outdated := reportDevstackResidue(os.Stdout, ctx.WorkspaceRoot.Value)

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
			fmt.Println("\nconfiguration drift (the service declares these keys in config.sources, and the local env does not supply them):")
		}
		drifted++
		fmt.Print(svcconfig.Render(name, entries))
	}
	return drifted
}
