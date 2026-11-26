package hostdaemon

import (
	"fmt"
	"io"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

func SharedRestarter(w io.Writer, wsName string) func(string) (bool, error) {
	client := tilt.NewClient("localhost", workspace.HostTiltPort)
	reloaded := false
	return func(svc string) (bool, error) {
		view, err := client.GetView()
		if err != nil {
			return false, nil
		}
		name := stack.ResourceName(wsName, svc, "")
		for _, r := range view.UiResources {
			if r.Metadata.Name != name || !runningResource(r) {
				continue
			}
			if !reloaded {
				reloaded = true
				for _, note := range SyncAndReload(client, ScopeAll()) {
					fmt.Fprintln(w, note)
				}
			}
			if out, err := client.RunCLI("trigger", name); err != nil {
				return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
			}
			return true, nil
		}
		return false, nil
	}
}

func runningResource(r tilt.UIResource) bool {
	if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
		return false
	}
	return r.Status.RuntimeStatus == "ok" || r.Status.RuntimeStatus == "pending"
}

func SyncShared(w io.Writer, ws *workspace.Workspace, restart func(string) (bool, error)) error {
	res, err := replica.SyncShared(ws, restart)
	if res == nil {
		return err
	}
	if len(res.Services) == 0 {
		fmt.Fprintf(w, "Workspace %q has no shared services. There is nothing to sync.\n", ws.Name)
		return err
	}
	fmt.Fprintf(w, "Replica for %q: %s\n", ws.Name, res.Root)
	for _, wt := range res.Created {
		fmt.Fprintf(w, "  ✓ replica worktree %-16s %s (%s)\n", wt.Repo, wt.Path, wt.Branch)
	}
	moved := 0
	for _, s := range res.Services {
		if s.Before == s.After {
			fmt.Fprintf(w, "  %-16s %-24s %s\n", s.Service, s.Ref, s.After)
			continue
		}
		moved++
		fmt.Fprintf(w, "  %-16s %-24s %s → %s\n", s.Service, s.Ref, s.Before, s.After)
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
	if moved == 0 {
		fmt.Fprintln(w, "Nothing moved. Every shared service was already at its default branch tip.")
	}
	if len(res.Restarted) > 0 {
		fmt.Fprintf(w, "✓ Restarted: %s\n", strings.Join(res.Restarted, ", "))
	}
	return err
}

func StartShared(w io.Writer, ws *workspace.Workspace) error {
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return err
	}
	names := template.SharedNames()
	if len(names) == 0 {
		return nil
	}
	client := tilt.NewClient("localhost", workspace.HostTiltPort)
	for _, note := range SyncAndReload(client, ScopeAll()) {
		fmt.Fprintln(w, note)
	}
	view, err := client.GetView()
	if err != nil {
		return fmt.Errorf("devstack can not reach the host daemon, so it starts no shared service: %w", err)
	}
	byName := map[string]tilt.UIResource{}
	for _, r := range view.UiResources {
		byName[r.Metadata.Name] = r
	}
	var started []string
	for _, n := range names {
		rn := stack.ResourceName(ws.Name, n, "")
		r, ok := byName[rn]
		if !ok || runningResource(r) {
			continue
		}
		if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
			if out, err := client.RunCLI("enable", rn); err != nil {
				return fmt.Errorf("devstack can not enable %s: %v\n%s", rn, err, out)
			}
		}
		if out, err := client.RunCLI("trigger", rn); err != nil {
			return fmt.Errorf("devstack can not start %s: %v\n%s", rn, err, out)
		}
		started = append(started, n)
	}
	if len(started) > 0 {
		fmt.Fprintf(w, "✓ Started the shared services: %s\n", strings.Join(started, ", "))
	}
	return nil
}
