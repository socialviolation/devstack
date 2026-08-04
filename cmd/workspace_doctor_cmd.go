package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/replica"
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

// reportWorkspaceDrift names the state that devstack keeps and that drifts: a
// repository devstack has not connected, a devstack file that nobody committed,
// and a workspace with no replica. It returns how many problems it found.
//
// None of these is a migration. Each one comes back when somebody adds a service
// or clones a repository, so the doctor is where they belong. The doctor reports
// and fixes nothing, and each report names the command that does fix it.
func reportWorkspaceDrift(w io.Writer, wsPath string) int {
	ws, err := workspace.FindByPath(wsPath)
	if err != nil {
		return 0
	}
	targets, _ := migrateTargets(ws)

	var unwired, uncommitted []migrateTarget
	for _, t := range targets {
		if wiringPending(t) {
			unwired = append(unwired, t)
		}
		if uncommittedAgentFiles(t.Dir) {
			uncommitted = append(uncommitted, t)
		}
	}

	found := 0
	if len(unwired) > 0 {
		found += len(unwired)
		fmt.Fprintf(w, "\ndevstack wiring: devstack is not connected to %s\n", pluralRepos(len(unwired)))
		for _, t := range unwired {
			fmt.Fprintf(w, "- [warn] %-24s %s\n", t.Label, t.Dir)
		}
		fmt.Fprintln(w, "  A repository is connected when it holds .mcp.json and the SessionStart hook.")
		fmt.Fprintln(w, "  To connect every service of this workspace, run: devstack init --all --claude-hook")
	}
	if len(uncommitted) > 0 {
		found += len(uncommitted)
		fmt.Fprintf(w, "\ndevstack files: %s a devstack file that nobody committed\n", holdPhrase(len(uncommitted)))
		for _, t := range uncommitted {
			fmt.Fprintf(w, "- [warn] %-24s %s\n", t.Label, t.Dir)
		}
		fmt.Fprintln(w, "  Read the diff in each repository. Then commit it there:")
		fmt.Fprintln(w, "  "+commitCommand)
		fmt.Fprintln(w, "  devstack does not commit for you, and it does not push.")
	}
	if !config.HasWorkspaceManifest(replica.Root(ws)) {
		found++
		fmt.Fprintf(w, "\nreplica: devstack has built no replica for this workspace, so base runs from your checkout\n")
		fmt.Fprintf(w, "- [warn] %s\n", replica.Root(ws))
		fmt.Fprintln(w, "  To build it and restart what runs, run: devstack workspace up")
		fmt.Fprintln(w, "  To build it and restart nothing, run: devstack base sync")
		fmt.Fprintln(w, "  To read what a replica is, and the other repairs it takes, run: devstack base --help")
	}
	return found
}

func holdPhrase(n int) string {
	if n == 1 {
		return "1 repository holds"
	}
	return fmt.Sprintf("%d repositories hold", n)
}

// wiringPending reads the two files that connect a repository to devstack, and
// reports whether either one is missing or out of date. It writes nothing: the
// doctor reports, and it changes no file.
func wiringPending(t migrateTarget) bool {
	if t.Service != "" && mcpPending(t.Dir, t.Service) {
		return true
	}
	return hookPending(t.Dir)
}

func mcpPending(dir, service string) bool {
	want, err := mcpJSONContent(dir, service)
	if err != nil {
		return true
	}
	got, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	return err != nil || !bytes.Equal(got, want)
}

func hookPending(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, claudeSettingsRel))
	if err != nil {
		return true
	}
	settings := map[string]any{}
	if json.Unmarshal(data, &settings) != nil {
		return true
	}
	hooks, _ := settings["hooks"].(map[string]any)
	sessionStart, _ := hooks["SessionStart"].([]any)
	_, changed := mergePrimeHook(sessionStart)
	return changed
}

// uncommittedAgentFiles reports whether dir holds an uncommitted change to a
// file devstack owns. git reads the index and the working tree only, and it
// reaches no network.
//
// The pathspec is relative to dir, so a service in a subdirectory of a
// repository reports its own files and never a sibling's.
func uncommittedAgentFiles(dir string) bool {
	args := append([]string{"status", "--porcelain", "--"}, devstackOwnedFiles()...)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(out)) > 0
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
	outdated += reportWorkspaceDrift(os.Stdout, ctx.WorkspaceRoot.Value)

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
