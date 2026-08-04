package cmd

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// transformRunningState moves what runs now onto the code the replica holds.
//
// An upgrade that installs a binary and builds a replica changes nothing that
// serves. The daemon keeps each copy on the process it started, out of the
// checkout it started from, until somebody restarts that copy. This step is the
// difference between an upgraded machine and an upgraded binary.
//
// It restarts the copies that run now, and it starts nothing. A user who stopped
// a service meant to stop it.
//
// wsName limits the restart to one workspace's copies. 'workspace up' moves one
// workspace's replica, so it must not restart another workspace's services. An
// upgrade replaces the binary for the machine, so it passes an empty name and
// reaches every copy.
func transformRunningState(w io.Writer, wsName string) error {
	client := tilt.NewClient("localhost", workspace.HostTiltPort)
	view, err := client.GetView()
	if err != nil {
		fmt.Fprintf(w, "The daemon does not run on :%d, so there is no running state to transform.\n", workspace.HostTiltPort)
		fmt.Fprintln(w, "devstack starts no daemon here. Your services run from the replica when you start them.")
		return nil
	}

	copies := runningBaseCopies(view, wsName)
	if len(copies) == 0 {
		fmt.Fprintln(w, "The daemon runs, and no base copy runs in it. devstack restarts nothing.")
		return nil
	}

	fmt.Fprintf(w, "%s run now. devstack restarts each one, so that it serves the code in the replica.\n", pluralCopies(len(copies)))
	fmt.Fprintln(w, "devstack restarts no copy that is stopped, and no copy of a feature stack.")
	fmt.Fprintln(w, "This step is slow. Each replica worktree is a new checkout, and a service can need its")
	fmt.Fprintln(w, "own dependency install before it serves again.")

	for _, note := range hostdaemon.SyncAndReload(client) {
		fmt.Fprintln(w, note)
	}

	restarted, errs := restartCopies(w, copies, func(name string) error {
		out, cerr := client.RunCLI("trigger", name)
		if cerr != nil {
			return fmt.Errorf("%v: %s", cerr, strings.TrimSpace(out))
		}
		return nil
	})
	writeRestartReport(w, restarted, readableReplicas())
	if len(errs) == 0 {
		return nil
	}
	fmt.Fprintf(w, "%s did not restart:\n", pluralCopies(len(errs)))
	for _, e := range errs {
		fmt.Fprintf(w, "  %v\n", e)
	}
	fmt.Fprintln(w, "To try one of these again, run: devstack service restart <svc> --stack base")
	return errors.Join(errs...)
}

// writeRestartReport states what each workspace's copies serve now.
//
// A copy serves the replica only when devstack can read that workspace's
// replica. Where it can not, generation falls back to the checkout, and the copy
// runs whatever work is parked there. One line for the whole machine can not say
// that, so the report is per workspace.
func writeRestartReport(w io.Writer, restarted []string, readable map[string]bool) {
	if len(restarted) == 0 {
		return
	}
	counts := map[string]int{}
	var names []string
	for _, copyName := range restarted {
		ws := workspaceOfCopy(copyName)
		if _, seen := counts[ws]; !seen {
			names = append(names, ws)
		}
		counts[ws]++
	}
	sort.Strings(names)

	fmt.Fprintf(w, "devstack restarted %s.\n", pluralCopies(len(restarted)))
	for _, name := range names {
		if readable[name] {
			fmt.Fprintf(w, "  %-16s %s. Each one serves the replica now.\n", name, pluralCopies(counts[name]))
			continue
		}
		fmt.Fprintf(w, "  %-16s %s. devstack can not read the replica of this workspace, so each one\n", name, pluralCopies(counts[name]))
		fmt.Fprintf(w, "  %-16s serves your checkout. To build the replica, run: devstack workspace up\n", "")
	}
}

// workspaceOfCopy reads the workspace name out of a copy name. The daemon names
// a base copy <workspace>:<service>.
func workspaceOfCopy(name string) string {
	if i := strings.Index(name, ":"); i >= 0 {
		return name[:i]
	}
	return name
}

// readableReplicas reports, for each registered workspace, whether devstack can
// read its replica. It asks the same question the generator asks, because the
// generator falls back to the checkout for every workspace it can not read.
func readableReplicas() map[string]bool {
	out := map[string]bool{}
	all, err := workspace.All()
	if err != nil {
		return out
	}
	for i := range all {
		_, err := replica.Resolve(&all[i])
		out[all[i].Name] = err == nil
	}
	return out
}

// runningBaseCopies names the base copies that the daemon serves now, in name
// order.
//
// It reads the daemon before anything else changes, because the set of copies to
// restart is the set that was running when the upgrade started.
//
// A feature stack runs out of its own worktree, and an upgrade does not move
// that worktree, so a stack copy is left alone.
func runningBaseCopies(view *tilt.TiltView, wsName string) []string {
	var out []string
	for _, r := range view.UiResources {
		if !isBaseCopy(r.Metadata.Name) || !isRunningCopy(r) {
			continue
		}
		if wsName != "" && !strings.HasPrefix(r.Metadata.Name, wsName+":") {
			continue
		}
		out = append(out, r.Metadata.Name)
	}
	sort.Strings(out)
	return out
}

// isBaseCopy reports whether a resource is a workspace's base copy. The daemon
// names a base copy <workspace>:<service>, and a stack copy
// <workspace>:<service>:<stack>.
func isBaseCopy(name string) bool {
	return strings.Count(name, ":") == 1
}

// isRunningCopy reports whether the daemon serves this copy now. A copy that is
// disabled is stopped, and a copy that has no process is not running. devstack
// restarts neither.
func isRunningCopy(r tilt.UIResource) bool {
	if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
		return false
	}
	switch r.Status.RuntimeStatus {
	case "ok", "pending":
		return true
	}
	return false
}

// restartCopies restarts each copy in turn and reports as it goes. It returns
// the copies that came back, and the failures.
//
// A restart can take minutes, so each copy is named before its restart starts
// and again when it ends. One copy that fails does not stop the copies after it:
// the failures are collected, and the caller reports them together.
func restartCopies(w io.Writer, names []string, restart func(string) error) (restarted []string, errs []error) {
	for i, name := range names {
		fmt.Fprintf(w, "  [%d/%d] %-28s restarts ...\n", i+1, len(names), name)
		if err := restart(name); err != nil {
			fmt.Fprintf(w, "  [%d/%d] %-28s FAILED: %v\n", i+1, len(names), name, err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		fmt.Fprintf(w, "  [%d/%d] %-28s restarted\n", i+1, len(names), name)
		restarted = append(restarted, name)
	}
	return restarted, errs
}

func pluralCopies(n int) string {
	if n == 1 {
		return "1 copy"
	}
	return fmt.Sprintf("%d copies", n)
}
