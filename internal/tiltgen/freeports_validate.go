package tiltgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
)

// portOwner is one resource's claim on one port: it binds it, so nothing else
// may kill whatever holds it.
type portOwner struct {
	Resource string
	Port     int
}

// freeClaim is one resource's intent to kill whatever holds a port before it
// starts.
type freeClaim struct {
	Resource string
	Port     int
}

// collectPortClaims walks every resource the host Tiltfile will contain and
// records what each one owns and what each one would free. Ownership comes from
// the port book, which is the same source that resolves ${self.port.<key>} — not
// from env URLs, which name the ports a service CALLS rather than the ports it
// binds.
func collectPortClaims(workspaces []WorkspaceGen) ([]portOwner, []freeClaim, error) {
	var owners []portOwner
	var claims []freeClaim

	visit := func(rw *config.ResolvedWorkspace, opts Options, prefix, namespace string) error {
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
				claims = append(claims, freeClaim{Resource: resource, Port: port})
			}
		}
		return nil
	}

	for _, w := range workspaces {
		if w.Base == nil || w.Base.Manifest == nil {
			continue
		}
		if err := visit(w.Base, w.BaseOpts, w.Name, ""); err != nil {
			return nil, nil, fmt.Errorf("workspace %q: %w", w.Name, err)
		}
		for _, s := range w.Stacks {
			if s.Workspace == nil || s.Workspace.Manifest == nil {
				continue
			}
			if err := visit(s.Workspace, s.Options, w.Name, s.Namespace); err != nil {
				return nil, nil, fmt.Errorf("workspace %q stack %q: %w", w.Name, s.Namespace, err)
			}
		}
	}
	return owners, claims, nil
}

// ValidateFreePorts refuses to generate a Tiltfile in which one resource would
// kill a port another resource binds.
//
// Both are supervised by the same daemon, so the kill does not stay a one-off:
// the victim is restarted, its own prep frees the port again, and the two
// resources flap against each other indefinitely. That failure presents as two
// services mysteriously restarting rather than as a port conflict, so it is
// worth refusing up front — the config is wrong, and it is cheap to say so here
// while both names are in hand.
func ValidateFreePorts(workspaces []WorkspaceGen) error {
	owners, claims, err := collectPortClaims(workspaces)
	if err != nil {
		return err
	}
	if len(claims) == 0 {
		return nil
	}

	ownersOf := map[int][]string{}
	for _, o := range owners {
		ownersOf[o.Port] = append(ownersOf[o.Port], o.Resource)
	}

	var conflicts []string
	seen := map[string]bool{}
	for _, c := range claims {
		var victims []string
		for _, owner := range ownersOf[c.Port] {
			if owner != c.Resource {
				victims = append(victims, owner)
			}
		}
		if len(victims) == 0 {
			continue
		}
		sort.Strings(victims)
		line := fmt.Sprintf("  %s frees port %d, which %s binds", c.Resource, c.Port, strings.Join(victims, " and "))
		if seen[line] {
			continue
		}
		seen[line] = true
		conflicts = append(conflicts, line)
	}
	if len(conflicts) == 0 {
		return nil
	}
	sort.Strings(conflicts)

	return fmt.Errorf("runtime.prep.freePorts would kill another service the daemon is running:\n%s\n\n"+
		"Both are supervised by the same daemon, so the victim restarts, frees the port back, and the two flap against each other.\n"+
		"Fix the duplicate port, or drop freePorts from one of them",
		strings.Join(conflicts, "\n"))
}
