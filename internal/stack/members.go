package stack

import (
	"fmt"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func planMembers(baseRW *config.ResolvedWorkspace, topo *config.TopologyGraph, wsName string, names []string) (named, groups, members []string, chains map[string][]string, err error) {
	shared := baseRW.SharedSet()
	for _, n := range names {
		if shared[n] {
			return nil, nil, nil, nil, sharedRefusal(baseRW, n)
		}
	}
	expanded, groups, err := expandGroups(topo, wsName, names)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for _, s := range expanded {
		if !shared[s] {
			named = append(named, s)
		}
	}
	if len(named) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("every service in %s is shared. The shared set runs once for the workspace: %s. Name a service that is not shared", strings.Join(names, ", "), strings.Join(baseRW.SharedNames(), ", "))
	}
	members, chains = topo.NeedsClosure(named, shared)
	return named, groups, members, chains, nil
}

func sharedRefusal(baseRW *config.ResolvedWorkspace, svc string) error {
	return fmt.Errorf("%q is a shared service. A shared service runs once for the workspace, and never in a stack. Every stack uses it. The shared set is: %s", svc, strings.Join(baseRW.SharedNames(), ", "))
}

func memberReasons(members []string, named map[string]bool, chains map[string][]string) []OverlayMember {
	out := make([]OverlayMember, 0, len(members))
	for _, s := range members {
		if named[s] {
			out = append(out, OverlayMember{Service: s, Reason: ReasonNamed})
			continue
		}
		out = append(out, OverlayMember{Service: s, Reason: ReasonNeeds, Chain: chains[s]})
	}
	return out
}

func (m OverlayMember) Describe() string {
	if m.Reason == ReasonNamed {
		return "named"
	}
	return "needed: " + strings.Join(m.Chain, " needs ")
}

func OutsideWarnings(baseRW *config.ResolvedWorkspace, members []string) []string {
	in := stringSet(members)
	shared := baseRW.SharedSet()
	users := map[string][]string{}
	for _, m := range members {
		var targets []string
		for _, e := range baseRW.CallEdges(m) {
			targets = append(targets, e.Service)
		}
		targets = append(targets, baseRW.RefTargets(m)...)
		seen := map[string]bool{}
		for _, t := range targets {
			if in[t] || shared[t] || seen[t] {
				continue
			}
			if _, ok := baseRW.Services[t]; !ok {
				continue
			}
			seen[t] = true
			users[t] = append(users[t], m)
		}
	}
	names := make([]string, 0, len(users))
	for t := range users {
		names = append(names, t)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, t := range names {
		out = append(out, fmt.Sprintf("%s is not in this stack. Run `stack add %s` if the change needs it. These members use %s: %s.", t, t, t, strings.Join(users[t], ", ")))
	}
	return out
}

func RecordWarnings(rec *Record) []string {
	base, err := workspace.FindByName(rec.Base)
	if err != nil {
		return nil
	}
	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return nil
	}
	return OutsideWarnings(baseRW, rec.Members)
}

func RequireShared(rw *config.ResolvedWorkspace, services []string, how string) error {
	for _, svc := range services {
		if _, ok := rw.Services[svc]; ok && !rw.IsShared(svc) {
			return fmt.Errorf("%q is not a shared service, so it runs only in a stack. Name the stack: %s", svc, how)
		}
	}
	return nil
}
