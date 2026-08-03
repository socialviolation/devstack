package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

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

func planReplicas(all []workspace.Workspace) []replicaPlan {
	out := make([]replicaPlan, 0, len(all))
	for i := range all {
		ws := all[i]
		p := replicaPlan{Name: ws.Name, Built: config.HasWorkspaceManifest(replica.Root(&ws))}
		template, err := config.ResolveWorkspace(ws.Path)
		if err != nil {
			p.Blocker = err
			out = append(out, p)
			continue
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
			out = append(out, p)
			continue
		}
		p.Repos = len(repos)
		out = append(out, p)
	}
	return out
}

func writeReplicaReport(w io.Writer, plans []replicaPlan, migrate bool) {
	if len(plans) == 0 {
		return
	}
	fmt.Fprintf(w, "\nBase now runs from a replica, and no longer from your checkout. Each workspace\nneeds one replica:\n")
	for _, p := range plans {
		switch {
		case p.Blocker != nil:
			fmt.Fprintf(w, "  %-16s devstack can not build a replica\n", p.Name)
			fmt.Fprintf(w, "      %s\n", strings.TrimSpace(p.Blocker.Error()))
		case p.Built:
			fmt.Fprintf(w, "  %-16s replica built, and holds %s\n", p.Name, countPhrase(p.Services, p.Repos))
		default:
			fmt.Fprintf(w, "  %-16s no replica yet. A build cuts %s\n", p.Name, countPhrase(p.Services, p.Repos))
		}
	}
	fmt.Fprintln(w, "\ndevstack cuts one git worktree for each repository. Each worktree is a new")
	fmt.Fprintln(w, "checkout. Before a service starts, its worktree needs its own dependency")
	fmt.Fprintln(w, "install: npm install, dotnet restore, or a virtual environment. A workspace of")
	fmt.Fprintln(w, "15 repositories pays that cost 15 times. Do this when you are not mid-task.")
	if !migrate {
		fmt.Fprintln(w, "\ndevstack upgrade --migrate builds these replicas. This command builds nothing.")
	}
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

// buildReplicas builds each workspace's replica by running bin, which is the
// devstack that was just installed and not this process.
//
// One workspace failing does not stop the rest: a machine with four replicas
// out of five is better than a machine with one, and the failures are named at
// the end.
func buildReplicas(bin string, all []workspace.Workspace) error {
	if len(all) == 0 {
		return nil
	}
	fmt.Println("\ndevstack builds the replica of each workspace. It starts nothing.")
	var failed []string
	for _, ws := range all {
		c := exec.Command(bin, "base", "build", "--workspace", ws.Name)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			failed = append(failed, ws.Name)
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", ws.Name, err)
			continue
		}
		fmt.Printf("✓ %s replica built\n", ws.Name)
	}
	if len(failed) > 0 {
		return fmt.Errorf("devstack did not build the replica of: %s. To read the error of one workspace, run: devstack base build --workspace <name>", strings.Join(failed, ", "))
	}
	return nil
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
