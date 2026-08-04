package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/replica"
)

// ensureReplicas builds the replica that base runs from, for each workspace that
// has none.
//
// A replica is machine state and not configuration: it is a git worktree for
// each repository, and a clone on another machine needs its own whatever any
// committed file says. So no migration owns it. 'workspace up' and 'workspace
// add' each build it, and 'workspace doctor' reports it missing. An upgrade
// builds it here, because an upgraded machine must have one.
//
// A workspace whose replica devstack can not build is a warning and not a stop:
// the workspaces after it still get theirs.
func ensureReplicas(w io.Writer) error {
	var errs []error
	all := migrate.Workspaces()
	for i := range all {
		ws := &all[i]
		if config.HasWorkspaceManifest(replica.Root(ws)) {
			continue
		}
		fmt.Fprintf(w, "\n%s: devstack builds the replica that base runs from.\n", ws.Name)
		fmt.Fprintln(w, "Each worktree is a new checkout, and it needs its own dependency install.")
		lines, res, err := replicaReport(ws)
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
		if err != nil {
			fmt.Fprintf(w, "  warning: devstack can not build this replica: %v\n", err)
			fmt.Fprintln(w, "  To build it after you fix the cause, run: devstack workspace up")
			errs = append(errs, fmt.Errorf("%s: %w", ws.Name, err))
			continue
		}
		for _, warn := range res.Warnings {
			fmt.Fprintf(w, "  warning: %s\n", warn)
		}
	}
	return errors.Join(errs...)
}

// writeDeprecations names the habits that still parse and no longer do what they
// did. Nothing else reports them: each one is a command that succeeds, or a file
// that is written, and the surprise arrives later.
func writeDeprecations(w io.Writer) {
	fmt.Fprint(w, `
This release changes what these habits do:

  You edit your checkout to change what base runs.
    → Put the change on the default branch. Then run: devstack workspace up
    → Or put the change in a stack, which runs your branch.
  You run: devstack service start|stop|restart <svc>
    → Add --stack base, or run the command in the copy's directory.
  You run: devstack env use <name>
    → Add --stack base, or run the command in the copy's directory.
  You run git pull in your checkout, then restart the service.
    → Run: devstack workspace up. It moves base, and it restarts what runs.
  You started your agent session before this upgrade.
    → Restart the session. It holds the tool list from before the upgrade.
`)
}
