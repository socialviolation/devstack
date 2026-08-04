// Package migrate runs devstack's migrations.
//
// A migration is a patch: one versioned unit of work, with a detector that says
// whether it is pending, a runner that applies it, and its own next action. Each
// patch answers for itself what a reader must do after it changed something,
// because "commit the diff" is the right instruction after a file sweep and the
// wrong one after a replica build.
//
// A patch runs one time in each workspace, and devstack records the run. It
// never becomes pending again. Continuous work is not a migration: a command
// that creates a thing configures the thing it creates, and 'workspace doctor'
// reports the state that drifts.
package migrate

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/socialviolation/devstack/internal/workspace"
)

// Workspaces is every workspace registered on this machine, in name order. One
// migration sweeps the whole machine, so this is the set each surface passes.
func Workspaces() []workspace.Workspace {
	all, err := workspace.All()
	if err != nil {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
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
		statuses, err := List(patches, all)
		if err != nil {
			return err
		}
		WriteList(w, statuses)
		return nil
	}
	fmt.Fprintf(w, "devstack runs %s over %s.\n", pluralMigrations(len(patches)), pluralWorkspaces(len(all)))
	return Apply(w, patches, all)
}

// WriteList prints every migration, applied or pending. It changes nothing.
func WriteList(w io.Writer, statuses []Status) {
	for _, st := range statuses {
		fmt.Fprintf(w, "\n%s  %s\n", st.ID, st.Title)
		for _, row := range st.Rows {
			switch {
			case row.Err != nil:
				fmt.Fprintf(w, "  %-16s blocked: %v\n", row.Name, row.Err)
			case row.Pending:
				fmt.Fprintf(w, "  %-16s pending: %s\n", row.Name, row.Why)
			case !row.AppliedAt.IsZero():
				fmt.Fprintf(w, "  %-16s applied on %s\n", row.Name, row.AppliedAt.Local().Format("2006-01-02 15:04"))
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
}

// Patch is one migration. It runs once for each workspace.
//
// Detect reads only. It reports whether the patch is pending, and a phrase that
// describes the state either way, which is what `upgrade` and `--list` print.
//
// Next is the instruction the reader gets while the patch leaves work to do. It
// receives the results of every workspace where the patch changed something, or
// where it left an Item the reader still has to act on. A patch that changed
// nothing and named nothing prints no instruction.
//
// A recorded patch never runs again, and never reports itself pending again.
// The record is the gate: it keeps an expensive run from being repeated, and it
// keeps a patch the user has undone on purpose from being applied a second time.
type Patch struct {
	ID     string
	Title  string
	Detect func(*workspace.Workspace) (pending bool, why string, err error)
	Run    func(*workspace.Workspace) (Result, error)
	Next   func([]Result) []string
}

// Apply runs every pending patch, in declared order, over every workspace.
//
// A patch that applies to nothing is reported and is not an error. A patch that
// fails does not stop the patches after it, nor the workspaces after it: the
// failures are collected and returned together.
func Apply(w io.Writer, patches []Patch, all []workspace.Workspace) error {
	recs, err := Load()
	if err != nil {
		return err
	}

	var errs []error
	var note []string
	for _, p := range patches {
		fmt.Fprintf(w, "\n%s  %s\n", p.ID, p.Title)
		var changed []Result
		for i := range all {
			ws := &all[i]
			res, err := applyOne(w, p, ws, recs)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %s: %w", p.ID, ws.Name, err))
			}
			if res.Changed {
				rec := Record{ID: p.ID, Workspace: ws.Name, AppliedAt: time.Now()}
				if aerr := Append(rec); aerr != nil {
					errs = append(errs, aerr)
				}
				recs = append(recs, rec)
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

	writeNote(w, note, len(errs))
	return errors.Join(errs...)
}

// applyOne runs one patch in one workspace, and prints what it did. It returns
// the result even when the run failed: a sweep that changed four files and then
// met a fifth it can not read has still changed four files, and the reader has
// to hear about them.
func applyOne(w io.Writer, p Patch, ws *workspace.Workspace, recs []Record) (Result, error) {
	if at := appliedAt(recs, p.ID, ws.Name); !at.IsZero() {
		fmt.Fprintf(w, "  %-16s applied already, on %s\n", ws.Name, at.Local().Format("2006-01-02 15:04"))
		return Result{Workspace: ws.Name}, nil
	}

	pending, why, err := p.Detect(ws)
	if err != nil {
		fmt.Fprintf(w, "  %-16s devstack can not read this workspace: %v\n", ws.Name, err)
		return Result{Workspace: ws.Name}, err
	}
	if !pending {
		fmt.Fprintf(w, "  %-16s nothing to do%s\n", ws.Name, phrase(why))
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
	}
	if !res.Changed && err == nil {
		fmt.Fprintln(w, "    devstack changed nothing")
	}
	return res, err
}

func phrase(why string) string {
	if why == "" {
		return ""
	}
	return ": " + why
}

// writeNote prints the next actions, last, so it is what an agent reads before
// it decides what to do. A run that changed nothing asks for nothing: an
// instruction to act on an empty diff teaches a reader to ignore the report.
//
// A failure changes nothing, so it reaches here with no next action. The closing
// line is the one an agent acts on, and "every migration is applied" after a
// patch failed is the one thing this output must never say.
func writeNote(w io.Writer, note []string, failed int) {
	if failed > 0 {
		fmt.Fprintln(w, "\nNEXT")
		fmt.Fprintf(w, "A MIGRATION FAILED. devstack did not apply %s. Read the failure above.\n", pluralMigrations(failed))
		fmt.Fprintln(w, "After you fix the cause, run this command again: devstack migrate")
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

// WorkspaceStatus is what one patch has done, and still has to do, in one
// workspace.
type WorkspaceStatus struct {
	Name      string
	AppliedAt time.Time
	Pending   bool
	Why       string
	Err       error
}

// Status is one patch across every workspace.
type Status struct {
	ID    string
	Title string
	Rows  []WorkspaceStatus
}

// Pending reports whether the patch still has work in any workspace.
func (s Status) Pending() bool {
	for _, r := range s.Rows {
		if r.Pending {
			return true
		}
	}
	return false
}

// List reports every patch, applied or pending. It reads only: `upgrade` and
// `migrate --list` both print it, and neither may change a file.
func List(patches []Patch, all []workspace.Workspace) ([]Status, error) {
	recs, err := Load()
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(patches))
	for _, p := range patches {
		st := Status{ID: p.ID, Title: p.Title}
		for i := range all {
			ws := &all[i]
			row := WorkspaceStatus{Name: ws.Name, AppliedAt: appliedAt(recs, p.ID, ws.Name)}
			if !row.AppliedAt.IsZero() {
				st.Rows = append(st.Rows, row)
				continue
			}
			row.Pending, row.Why, row.Err = p.Detect(ws)
			st.Rows = append(st.Rows, row)
		}
		out = append(out, st)
	}
	return out, nil
}
