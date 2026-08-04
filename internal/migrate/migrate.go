// Package migrate moves a workspace configuration from one version to the next.
//
// A migration is a patch: it declares the version it moves from and the version
// it moves to, and it runs when the workspace manifest is at the from version.
// After it runs, devstack writes the to version into that manifest.
//
// The version is the whole of the state, and it lives in the manifest, which is
// committed. So a teammate who clones a repository that somebody migrated
// already gets the answer with the clone, and devstack asks them for nothing.
//
// Machine state is not a migration. A git worktree, a file that nobody
// committed, and a repository that devstack is not connected to all come back
// when somebody adds a service. 'devstack workspace doctor' reports those.
package migrate

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

// Stamp names the devstack that migrates. It goes in the manifest beside the
// version, for a person who reads the file. The cmd package sets it from the
// build stamp: this package must not decide how a binary names itself.
var Stamp = "devstack"

// Workspaces is every workspace registered on this machine, in name order. One
// migration sweeps the whole machine, so this is the set each surface passes.
//
// A registry devstack can not read is an error and not an empty machine. An
// empty set reads as "there is nothing to migrate", and a caller that reports
// that has told the user their upgrade is complete when it did nothing.
func Workspaces() ([]workspace.Workspace, error) {
	all, err := workspace.All()
	if err != nil {
		return nil, fmt.Errorf("devstack can not read the workspace registry, so it migrates nothing: %w", err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

// Sweep writes one migration report. It is the whole of what a caller reads:
// the patches, what each one did or still has to do, and the next action.
//
// run=false previews, and it changes no file. run=true applies every pending
// patch. The CLI and the MCP tool both call this, so neither one can print a
// report the other does not.
func Sweep(w io.Writer, patches []Patch, all []workspace.Workspace, run bool) error {
	if len(all) == 0 {
		fmt.Fprintln(w, "No workspace is registered on this machine, so devstack migrates nothing.")
		return nil
	}
	if !run {
		WriteList(w, List(patches, all))
		return nil
	}
	fmt.Fprintf(w, "devstack runs %s over %s.\n", pluralMigrations(len(patches)), pluralWorkspaces(len(all)))
	return Apply(w, patches, all)
}

// WriteList prints every migration, pending or done. It changes nothing.
func WriteList(w io.Writer, statuses []Status) {
	for _, st := range statuses {
		fmt.Fprintf(w, "\n%s  %s\n", st.Name(), st.Title)
		for _, row := range st.Rows {
			switch {
			case row.Err != nil:
				fmt.Fprintf(w, "  %-16s blocked: %v\n", row.Name, row.Err)
			case row.Pending:
				fmt.Fprintf(w, "  %-16s pending: %s\n", row.Name, row.Why)
			default:
				fmt.Fprintf(w, "  %-16s nothing to do: %s\n", row.Name, row.Why)
			}
		}
	}
	fmt.Fprintln(w, "\ndevstack migrate runs each pending migration. This command changes nothing.")
}

func pluralWorkspaces(n int) string {
	if n == 1 {
		return "1 workspace"
	}
	return fmt.Sprintf("%d workspaces", n)
}

// Item is one thing a patch changed, named so the reader can act on it. A count
// with no path beside it tells a reader nothing.
type Item struct {
	Label string
	Path  string
}

// Result is what one patch did in one workspace.
type Result struct {
	Workspace string
	Changed   bool
	Lines     []string
	Items     []Item

	// Incomplete is true when the patch left something that only a human can
	// finish. devstack writes no new version for an incomplete run, so the
	// migration stays pending and the remedy that names the file keeps working.
	Incomplete bool
}

// Patch is one migration: the step from one version of the workspace manifest
// to the next.
//
// From and To are that step. A patch runs in a workspace whose manifest is at
// From, and after it succeeds devstack writes To into that manifest. The version
// is the gate: it stops a second run, and it travels with the repository, so a
// clone of a migrated repository runs nothing.
//
// Next is the instruction the reader gets while the patch leaves work to do. It
// receives the results of every workspace where the patch changed something, or
// where it left an Item the reader still has to act on.
type Patch struct {
	From  int
	To    int
	Title string
	Run   func(*workspace.Workspace) (Result, error)
	Next  func([]Result) []string
}

// Name is how a report calls this patch.
func (p Patch) Name() string { return fmt.Sprintf("version %d to %d", p.From, p.To) }

// Apply runs every pending patch, in declared order, over every workspace.
//
// A patch that applies to nothing is reported and is not an error. A patch that
// fails does not stop the patches after it, nor the workspaces after it: the
// failures are collected and returned together.
func Apply(w io.Writer, patches []Patch, all []workspace.Workspace) error {
	var errs []error
	var note []string
	incomplete := 0
	for _, p := range patches {
		fmt.Fprintf(w, "\n%s  %s\n", p.Name(), p.Title)
		var changed []Result
		for i := range all {
			ws := &all[i]
			res, err := applyOne(w, p, ws)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %s: %w", p.Name(), ws.Name, err))
			}
			if res.Incomplete {
				incomplete++
			}
			if !res.Changed && len(res.Items) == 0 {
				continue
			}
			changed = append(changed, res)
		}
		if len(changed) == 0 || p.Next == nil {
			continue
		}
		if len(note) > 0 {
			note = append(note, "")
		}
		note = append(note, p.Next(changed)...)
	}

	writeNote(w, note, len(errs), incomplete)
	return errors.Join(errs...)
}

// applyOne runs one patch in one workspace, and prints what it did. It returns
// the result even when the run failed: a sweep that changed four files and then
// met a fifth it can not read has still changed four files, and the reader has
// to hear about them.
//
// devstack writes the new version only after the run succeeded and finished. A
// failed run, and a run that left work for a human, both leave the old version
// in the manifest, so the next run tries again.
func applyOne(w io.Writer, p Patch, ws *workspace.Workspace) (Result, error) {
	version, err := config.WorkspaceVersion(ws.Path)
	if err != nil {
		fmt.Fprintf(w, "  %-16s devstack can not read this workspace: %v\n", ws.Name, err)
		return Result{Workspace: ws.Name}, err
	}
	if version != p.From {
		fmt.Fprintf(w, "  %-16s nothing to do: %s\n", ws.Name, why(version, p))
		return Result{Workspace: ws.Name}, nil
	}

	fmt.Fprintf(w, "  %s\n", ws.Name)
	res, err := p.Run(ws)
	res.Workspace = ws.Name
	for _, l := range res.Lines {
		fmt.Fprintln(w, l)
	}
	if err != nil {
		fmt.Fprintf(w, "    failed: %v\n", err)
		return res, err
	}
	if res.Incomplete {
		return res, nil
	}
	if err := config.SetWorkspaceVersion(ws.Path, p.To, Stamp); err != nil {
		fmt.Fprintf(w, "    failed: %v\n", err)
		return res, err
	}
	fmt.Fprintf(w, "    %-24s is at version %d now\n", config.WorkspaceManifestFileName, p.To)

	res.Changed = true
	res.Items = withItem(res.Items, Item{Label: "workspace root", Path: ws.Path})
	return res, nil
}

// withItem adds an item unless that path is named already. The version goes in a
// file that a human commits, so the workspace root is always work to finish, and
// a patch that wrote in that same directory must not name it twice.
func withItem(items []Item, add Item) []Item {
	for _, it := range items {
		if it.Path == add.Path {
			return items
		}
	}
	return append(items, add)
}

// why states where a workspace stands against one patch, in versions.
func why(version int, p Patch) string {
	switch {
	case version == 0:
		return fmt.Sprintf("there is no %s here", config.WorkspaceManifestFileName)
	case version == p.From:
		return fmt.Sprintf("this workspace is at version %d, and this devstack needs version %d", version, p.To)
	default:
		return fmt.Sprintf("this workspace is at version %d", version)
	}
}

// writeNote prints the next actions, last, so it is what an agent reads before
// it decides what to do. A run that changed nothing asks for nothing: an
// instruction to act on an empty diff teaches a reader to ignore the report.
//
// A failure changes nothing, so it reaches here with no next action. The closing
// line is the one an agent acts on, and "every migration is applied" after a
// patch failed is the one thing this output must never say.
//
// incomplete counts the workspaces where the patch left work that only a person
// can do. devstack writes no new version there, so the migration is still
// pending, and the closing line must send the reader back to the file.
func writeNote(w io.Writer, note []string, failed, incomplete int) {
	if failed > 0 {
		fmt.Fprintln(w, "\nNEXT")
		fmt.Fprintf(w, "A MIGRATION FAILED. devstack did not apply %s. Read the failure above.\n", pluralMigrations(failed))
		fmt.Fprintln(w, "After you fix the cause, run this command again: devstack migrate")
		for _, l := range note {
			fmt.Fprintln(w, l)
		}
		return
	}
	if incomplete > 0 {
		fmt.Fprintln(w, "\nNEXT")
		fmt.Fprintf(w, "A MIGRATION IS NOT FINISHED. devstack found a file that only you can change, in %s.\n", pluralWorkspaces(incomplete))
		fmt.Fprintln(w, "This migration stays pending until that file is clean.")
		fmt.Fprintln(w, "The report above names each file. Remove the devstack block from it by hand.")
		fmt.Fprintln(w, "Then run this command again: devstack migrate")
		for _, l := range note {
			fmt.Fprintln(w, l)
		}
		return
	}
	if len(note) == 0 {
		fmt.Fprintln(w, "\ndevstack changed nothing. Every migration is applied. Do nothing.")
		return
	}
	fmt.Fprintln(w, "\nNEXT")
	for _, l := range note {
		fmt.Fprintln(w, l)
	}
}

func pluralMigrations(n int) string {
	if n == 1 {
		return "1 migration"
	}
	return fmt.Sprintf("%d migrations", n)
}

// WorkspaceStatus is where one workspace stands against one patch.
type WorkspaceStatus struct {
	Name    string
	Version int
	Pending bool
	Why     string
	Err     error
}

// Status is one patch across every workspace.
type Status struct {
	From  int
	To    int
	Title string
	Rows  []WorkspaceStatus
}

// Name is how a report calls this patch.
func (s Status) Name() string { return fmt.Sprintf("version %d to %d", s.From, s.To) }

// Pending reports whether the patch still has work in any workspace.
func (s Status) Pending() bool {
	for _, r := range s.Rows {
		if r.Pending {
			return true
		}
	}
	return false
}

// List reports every patch against every workspace. It reads only: `upgrade` and
// `migrate --list` both print it, and neither may change a file.
func List(patches []Patch, all []workspace.Workspace) []Status {
	out := make([]Status, 0, len(patches))
	for _, p := range patches {
		st := Status{From: p.From, To: p.To, Title: p.Title}
		for i := range all {
			ws := &all[i]
			row := WorkspaceStatus{Name: ws.Name}
			row.Version, row.Err = config.WorkspaceVersion(ws.Path)
			if row.Err == nil {
				row.Pending = row.Version == p.From
				row.Why = why(row.Version, p)
			}
			st.Rows = append(st.Rows, row)
		}
		out = append(out, st)
	}
	return out
}
