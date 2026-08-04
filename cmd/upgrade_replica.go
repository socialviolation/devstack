package cmd

import (
	"fmt"
	"io"
	"sort"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

// replicaPatch builds the replica that base runs from: one git worktree for
// each repository of the workspace.
//
// It does not rescan. A build cuts a worktree for every repository, and each
// worktree needs its own dependency install, so it is expensive to repeat. A
// user who removes a replica did that on purpose, and a migration must not undo
// the decision. The record decides, and the filesystem is read only to keep a
// replica that is built already from being built twice.
func replicaPatch() migrate.Patch {
	return migrate.Patch{
		ID:     "0.2.0-replica",
		Title:  "Build the replica that base runs from",
		Detect: detectReplica,
		Run:    runReplicaPatch,
		Next:   nextReplica,
	}
}

func detectReplica(ws *workspace.Workspace) (bool, string, error) {
	p := planReplica(ws)
	if p.Blocker != nil {
		return false, "", p.Blocker
	}
	if p.Built {
		return false, fmt.Sprintf("the replica is built, and holds %s", countPhrase(p.Services, p.Repos)), nil
	}
	return true, fmt.Sprintf("no replica yet. A build cuts %s, and each worktree needs its own dependency install", countPhrase(p.Services, p.Repos)), nil
}

func runReplicaPatch(ws *workspace.Workspace) (migrate.Result, error) {
	lines, res, err := replicaReport(ws)
	if err != nil {
		return migrate.Result{}, err
	}
	for _, w := range res.Warnings {
		lines = append(lines, "  warning: "+w)
	}
	out := migrate.Result{Lines: indent(lines, "  ")}
	out.Changed = len(res.Created) > 0 || len(res.Removed) > 0
	if out.Changed {
		out.Items = []migrate.Item{{Label: ws.Name, Path: replica.Root(ws)}}
	}
	return out, nil
}

// nextReplica is the instruction after a replica was built. A service that runs
// now still serves the old code, and nothing else tells the reader that.
func nextReplica(results []migrate.Result) []string {
	out := []string{"RESTART YOUR SERVICES. base runs from the replica now, and not from your checkout.", "These workspaces have a new replica:"}
	for _, r := range results {
		for _, it := range r.Items {
			out = append(out, fmt.Sprintf("  %-16s %s", it.Label, it.Path))
		}
	}
	return append(out,
		"A service that runs now still serves the old code. To move one service to the",
		"replica, run:",
		"  devstack service restart <svc> --stack base",
		"Each worktree is a new checkout. Before a service starts, its worktree needs its",
		"own dependency install: npm install, dotnet restore, or a virtual environment.",
		"Your checkout is the template, and base does not read it. An edit there reaches",
		"base after two steps:",
		"  1. Put the edit on the default branch.",
		"  2. Run: devstack base sync")
}

func indent(lines []string, pad string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, pad+l)
	}
	return out
}

// replicaPlan is what a replica of one workspace will hold, read from the
// manifest and from git. A plan is a report and never a build: the report runs
// before the reader has agreed to anything, and a build cuts a worktree for
// every repository.
type replicaPlan struct {
	Name     string
	Built    bool
	Services int
	Repos    int
	Blocker  error
}

func planReplica(ws *workspace.Workspace) replicaPlan {
	p := replicaPlan{Name: ws.Name, Built: config.HasWorkspaceManifest(replica.Root(ws))}
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		p.Blocker = err
		return p
	}
	names := make([]string, 0, len(template.Services))
	for name := range template.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	p.Services = len(names)

	repos, err := worktree.Plan(names, func(s string) string { return template.Services[s].RepoPath }, nil)
	if err != nil {
		p.Blocker = err
		return p
	}
	p.Repos = len(repos)
	return p
}

func countPhrase(services, repos int) string {
	svc, repo := "services", "repositories"
	if services == 1 {
		svc = "service"
	}
	if repos == 1 {
		repo = "repository"
	}
	return fmt.Sprintf("%d %s in %d %s", services, svc, repos, repo)
}

// writeDeprecations names the habits that still parse and no longer do what they
// did. Nothing else reports them: each one is a command that succeeds, or a file
// that is written, and the surprise arrives later.
func writeDeprecations(w io.Writer) {
	fmt.Fprint(w, `
This release changes what these habits do:

  You edit your checkout to change what base runs.
    → Put the change on the default branch. Then run: devstack base sync
    → Or put the change in a stack, which runs your branch.
  You run: devstack service start|stop|restart <svc>
    → Add --stack base, or run the command in the copy's directory.
  You run: devstack env use <name>
    → Add --stack base, or run the command in the copy's directory.
  You run git pull in your checkout, then restart the service.
    → Run: devstack base sync. Then restart the service.
  You started your agent session before this upgrade.
    → Restart the session. It holds the tool list from before the upgrade.
`)
}
