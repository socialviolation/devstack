// Package migrate runs devstack's migrations.
//
// A migration is a patch: one versioned unit of work, with a detector that says
// whether it is pending, a runner that applies it, and its own next action. Each
// patch answers for itself what a reader must do after it changed something,
// because "commit the diff" is the right instruction after a file sweep and the
// wrong one after a replica build.
package migrate

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/socialviolation/devstack/internal/workspace"
)

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
// Next is the instruction the reader gets after the patch changed something. It
// receives the results of every workspace the patch changed, and nothing else:
// a patch that changed nothing prints no instruction.
//
// Rescan makes the filesystem authoritative. A record then never suppresses the
// run, and Detect alone decides. Set it where the run is cheap and idempotent,
// so a lost or stale record can never stand between a user and a correct tree.
// Leave it false where the run is expensive, or where the user may have undone
// the patch on purpose and must not have it done again.
type Patch struct {
	ID     string
	Title  string
	Rescan bool
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
			if !res.Changed {
				continue
			}
			rec := Record{ID: p.ID, Workspace: ws.Name, AppliedAt: time.Now()}
			if aerr := Append(rec); aerr != nil {
				errs = append(errs, aerr)
			}
			recs = append(recs, rec)
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

	writeNote(w, note)
	return errors.Join(errs...)
}

// applyOne runs one patch in one workspace, and prints what it did. It returns
// the result even when the run failed: a sweep that changed four files and then
// met a fifth it can not read has still changed four files, and the reader has
// to hear about them.
func applyOne(w io.Writer, p Patch, ws *workspace.Workspace, recs []Record) (Result, error) {
	if !p.Rescan {
		if at := appliedAt(recs, p.ID, ws.Name); !at.IsZero() {
			fmt.Fprintf(w, "  %-16s applied already, on %s\n", ws.Name, at.Local().Format("2006-01-02 15:04"))
			return Result{Workspace: ws.Name}, nil
		}
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
func writeNote(w io.Writer, note []string) {
	if len(note) == 0 {
		fmt.Fprintln(w, "\ndevstack changed nothing. Every migration is applied. Do nothing.")
		return
	}
	fmt.Fprintln(w, "\nNEXT")
	for _, l := range note {
		fmt.Fprintln(w, l)
	}
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
			if !p.Rescan && !row.AppliedAt.IsZero() {
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
