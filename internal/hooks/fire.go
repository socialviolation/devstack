package hooks

import (
	"fmt"
	"io"
	"sort"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// Context resolves everything a hook reads for one event. For a stack it
// resolves the stack's worktree workspace and its allocated ports, so a service
// hook runs in the worktree and ${self.url} names the port that instance is
// actually on. Workspace-level hooks always come from the base manifest: a stack
// inherits them, exactly as it inherits base's environments.
//
// An empty services list defaults to the stack's overlay, or to every service in
// the workspace when there is no stack.
func Context(ws *workspace.Workspace, stackName, event string, services []string) (Event, Source, error) {
	if ws == nil {
		return Event{}, Source{}, fmt.Errorf("no workspace resolved")
	}
	baseRW, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return Event{}, Source{}, fmt.Errorf("failed to resolve workspace %q: %w", ws.Name, err)
	}

	ev := Event{
		Name:          event,
		WorkspaceName: ws.Name,
		StackRoot:     ws.Path,
		EnvName:       baseRW.Manifest.Workspace.Env,
		Book:          config.BuildPortBook(baseRW),
	}
	rw := baseRW

	if stackName != "" && stackName != "base" {
		rec, err := stack.Resolve(ws.Name, stackName)
		if err != nil {
			return Event{}, Source{}, err
		}
		rw, err = stack.ResolveWorktree(rec)
		if err != nil {
			return Event{}, Source{}, err
		}
		names := make([]string, 0, len(rw.Services))
		for n := range rw.Services {
			names = append(names, n)
		}
		opts, err := stack.GenerateOptions(rec, names)
		if err != nil {
			return Event{}, Source{}, err
		}
		ev.Stack = rec.Name
		ev.StackRoot = rec.Root
		ev.Branch = rec.Branch
		ev.Book = opts.Book
		if rec.Env != "" {
			ev.EnvName = rec.Env
		}
		if len(services) == 0 {
			services = append([]string(nil), rec.Overlay...)
		}
	}

	if len(services) == 0 {
		for n := range rw.Services {
			services = append(services, n)
		}
	}
	sort.Strings(services)
	ev.Services = services

	return ev, BuildSource(baseRW.Manifest, ws.Path, rw), nil
}

// Fire runs an event's hooks and reports what happened. A setup event's failure
// is returned and fails whatever fired it; a teardown event's is reported and
// swallowed, so a broken hook can never leave a stack that will not come down.
//
// Every caller that changes lifecycle state must fire the matching event — the
// CLI and the MCP tools reach the same underlying operations by different
// routes, and a hook that fires for one but not the other is worse than no hook.
func Fire(ws *workspace.Workspace, stackName, event string, services []string, w io.Writer) error {
	ev, src, err := Context(ws, stackName, event, services)
	if err != nil {
		if config.IsTeardownEvent(event) {
			fmt.Fprintf(w, "warning: could not resolve hooks for %s: %v\n", event, err)
			return nil
		}
		return fmt.Errorf("could not resolve hooks for %s: %w", event, err)
	}
	_, err = Run(ev, src, w)
	return err
}
