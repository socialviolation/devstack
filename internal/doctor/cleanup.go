package doctor

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

type Action struct {
	What string
	Err  error
	Kept bool
}

func Cleanup(ws *workspace.Workspace, apply bool, h Host) ([]Action, error) {
	recs, err := stack.LoadStore(ws.Name)
	if err != nil {
		return nil, err
	}
	o := findOrphans(ws, recs, h)
	var out []Action
	do := func(what string, fn func() error) bool {
		a := Action{What: what}
		if apply {
			a.Err = fn()
		}
		out = append(out, a)
		return a.Err == nil
	}

	for _, rec := range o.records {
		do(fmt.Sprintf("remove the record of stack %s, whose directory %s is gone", rec.Name, rec.Root), func() error {
			_, err := stack.Remove(ws, rec.Name, true)
			return err
		})
	}
	for _, d := range o.dirs {
		if len(d.dirty) > 0 {
			out = append(out, Action{Kept: true, What: fmt.Sprintf("keep %s: the worktree %s holds uncommitted work", d.path, strings.Join(d.dirty, ", "))})
			continue
		}
		removed := true
		for _, wt := range d.worktrees {
			removed = do(fmt.Sprintf("remove the worktree %s of %s", wt.path, wt.repo), func() error { return h.RemoveWorktree(wt.path) }) && removed
		}
		if removed {
			do("remove the directory "+d.path, func() error { return os.RemoveAll(d.path) })
		}
	}
	for _, key := range o.ports {
		do("release the port allocation "+workspace.DataDir(key)+"ports.json", func() error { return workspace.ReleasePorts(key) })
	}
	repos := map[string]bool{}
	for _, wt := range o.prunable {
		repos[wt.repo] = true
	}
	for _, repo := range sortedKeys(repos) {
		do("prune the worktrees git lists in "+repo+" whose directory is gone", func() error { return h.Prune(repo) })
	}
	sort.SliceStable(out, func(i, j int) bool { return !out[i].Kept && out[j].Kept })
	return out, nil
}

func RenderCleanup(actions []Action, apply bool, next string) string {
	if len(actions) == 0 {
		return "nothing to clean up: no orphans found\n"
	}
	var b strings.Builder
	for _, a := range actions {
		switch {
		case a.Kept:
			fmt.Fprintf(&b, "- %s\n", a.What)
		case !apply:
			fmt.Fprintf(&b, "- would %s\n", a.What)
		case a.Err != nil:
			fmt.Fprintf(&b, "- failed: %s: %v\n", a.What, a.Err)
		default:
			fmt.Fprintf(&b, "- done: %s\n", a.What)
		}
	}
	if !apply {
		b.WriteString("dry run: nothing changed. " + next + "\n")
	}
	return b.String()
}
