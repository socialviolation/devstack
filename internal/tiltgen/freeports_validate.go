package tiltgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/ports"
)

// portOwner is one resource's claim on one port: it binds it, so nothing else
// may kill whatever holds it.
type portOwner struct {
	Resource string
	Port     int
}

// slot locates one rendered block in the host Tiltfile: a workspace's own base
// services, or one of its stacks. It is how a conflict is traced back to the
// instance that declared the reclaim, so only that instance loses it.
type slot struct {
	ws    int
	stack int // -1 for the workspace's base services
}

// freeClaim is one resource's intent to kill whatever holds a port before it
// starts.
type freeClaim struct {
	Resource string
	Service  string
	Port     int
	slot     slot
}

// FreePortConflict is one reclaim devstack will not generate: it would kill a
// port another resource in the same daemon binds, or it names a privileged port
// that `devstack ports free` refuses.
type FreePortConflict struct {
	Resource string
	Service  string
	Port     int
	Victims  []string
	slot     slot
}

// Warning is what devstack says when it drops a reclaim.
func (c FreePortConflict) Warning() string {
	if len(c.Victims) == 0 {
		return fmt.Sprintf("%s frees port %d, and devstack never reclaims a port below %d. devstack dropped that reclaim.\n"+
			"  %s still starts, but it does not free port %d. Remove that port from freePorts.",
			c.Resource, c.Port, PrivilegedPort, c.Resource, c.Port)
	}
	return fmt.Sprintf("%s frees port %d, which %s binds. devstack dropped that reclaim.\n"+
		"  Both run under the same daemon, so the victim restarts, frees the port back, and the two flap forever.\n"+
		"  %s still starts, but it does not free port %d. Fix the duplicate port, or remove freePorts from one of them.",
		c.Resource, c.Port, strings.Join(c.Victims, " and "), c.Resource, c.Port)
}

// PrivilegedPort is the boundary below which devstack never reclaims: a listener
// there is far more likely to be a system service than a dev server someone
// forgot to stop.
const PrivilegedPort = ports.Privileged

// collectPortClaims walks every resource the host Tiltfile will contain and
// records what each one owns and what each one would free. Ownership comes from
// the port book, which is the same source that resolves ${self.port.<key>} — not
// from env URLs, which name the ports a service CALLS rather than the ports it
// binds.
func collectPortClaims(workspaces []WorkspaceGen) ([]portOwner, []freeClaim, error) {
	var owners []portOwner
	var claims []freeClaim

	visit := func(rw *config.ResolvedWorkspace, opts Options, prefix, namespace string, at slot) error {
		book := opts.Book
		if book == nil {
			book = config.BuildPortBook(rw)
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			svc := rw.Services[name]
			if svc.Manifest == nil {
				continue
			}
			resource := hostName(prefix, name, namespace)
			for _, port := range book[name] {
				owners = append(owners, portOwner{Resource: resource, Port: port})
			}
			spec := svc.Manifest.Runtime.Prep.FreePorts
			if !spec.Enabled() {
				continue
			}
			targets, err := spec.Resolve(name, book[name])
			if err != nil {
				return err
			}
			for _, port := range targets {
				claims = append(claims, freeClaim{Resource: resource, Service: name, Port: port, slot: at})
			}
		}
		return nil
	}

	for i, w := range workspaces {
		if w.Base == nil || w.Base.Manifest == nil {
			continue
		}
		if err := visit(w.Base, w.BaseOpts, w.Name, "", slot{ws: i, stack: -1}); err != nil {
			return nil, nil, fmt.Errorf("workspace %q: %w", w.Name, err)
		}
		for j, s := range w.Stacks {
			if s.Workspace == nil || s.Workspace.Manifest == nil {
				continue
			}
			if err := visit(s.Workspace, s.Options, w.Name, s.Namespace, slot{ws: i, stack: j}); err != nil {
				return nil, nil, fmt.Errorf("workspace %q stack %q: %w", w.Name, s.Namespace, err)
			}
		}
	}
	return owners, claims, nil
}

// FreePortConflicts reports every reclaim that would kill a port another
// resource in the same daemon binds.
//
// Both are supervised by the same daemon, so the kill does not stay a one-off:
// the victim is restarted, its own prep frees the port again, and the two
// resources flap against each other indefinitely. That failure presents as two
// services mysteriously restarting rather than as a port conflict, which is why
// devstack looks for it while both names are still in hand.
func FreePortConflicts(workspaces []WorkspaceGen) ([]FreePortConflict, error) {
	owners, claims, err := collectPortClaims(workspaces)
	if err != nil {
		return nil, err
	}
	if len(claims) == 0 {
		return nil, nil
	}

	ownersOf := map[int][]string{}
	for _, o := range owners {
		ownersOf[o.Port] = append(ownersOf[o.Port], o.Resource)
	}

	var conflicts []FreePortConflict
	seen := map[string]bool{}
	for _, c := range claims {
		var victims []string
		for _, owner := range ownersOf[c.Port] {
			if owner != c.Resource {
				victims = append(victims, owner)
			}
		}
		if len(victims) == 0 && c.Port >= ports.Privileged {
			continue
		}
		sort.Strings(victims)
		key := fmt.Sprintf("%s/%d", c.Resource, c.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		conflicts = append(conflicts, FreePortConflict{
			Resource: c.Resource, Service: c.Service, Port: c.Port, Victims: victims, slot: c.slot,
		})
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Resource != conflicts[j].Resource {
			return conflicts[i].Resource < conflicts[j].Resource
		}
		return conflicts[i].Port < conflicts[j].Port
	})
	return conflicts, nil
}

// dropConflictingReclaims returns the workspaces with each conflicting reclaim
// suppressed, plus one warning per conflict.
//
// Only the instance that declares the reclaim loses it. The host Tiltfile
// composes every registered workspace, so failing generation on a collision took
// away 'workspace up', 'service stop', 'env set' and 'stack rm' from every other
// workspace on the machine — over a manifest their owners cannot edit.
func dropConflictingReclaims(workspaces []WorkspaceGen, conflicts []FreePortConflict) ([]WorkspaceGen, []string) {
	if len(conflicts) == 0 {
		return workspaces, nil
	}

	out := make([]WorkspaceGen, len(workspaces))
	copy(out, workspaces)
	for i := range out {
		out[i].Stacks = append([]StackGen(nil), out[i].Stacks...)
	}

	warnings := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		warnings = append(warnings, c.Warning())
		if c.slot.ws < 0 || c.slot.ws >= len(out) {
			continue
		}
		if c.slot.stack < 0 {
			out[c.slot.ws].BaseOpts = out[c.slot.ws].BaseOpts.skipping(c.Service, c.Port)
			continue
		}
		if c.slot.stack < len(out[c.slot.ws].Stacks) {
			s := &out[c.slot.ws].Stacks[c.slot.stack]
			s.Options = s.Options.skipping(c.Service, c.Port)
		}
	}
	return out, warnings
}
