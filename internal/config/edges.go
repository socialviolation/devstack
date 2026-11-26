package config

import (
	"fmt"
	"sort"
	"strings"
)

func (rw *ResolvedWorkspace) Needs(svc string) []ServiceEdge {
	s, ok := rw.Services[svc]
	if !ok || s.Manifest == nil {
		return nil
	}
	return s.Manifest.Needs
}

func (rw *ResolvedWorkspace) CallEdges(svc string) []ServiceEdge {
	var out []ServiceEdge
	seen := map[string]bool{}
	add := func(e ServiceEdge) {
		if e.Service == "" || seen[e.Service] {
			return
		}
		seen[e.Service] = true
		out = append(out, e)
	}
	if s, ok := rw.Services[svc]; ok && s.Manifest != nil {
		for _, e := range s.Manifest.Calls {
			add(e)
		}
	}
	if m := rw.Manifest; m != nil {
		for _, name := range unionSorted(m.Dependencies[svc], m.StartsAfter[svc], m.Calls[svc]) {
			add(ServiceEdge{Service: name})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

func (rw *ResolvedWorkspace) ResourceDeps(svc string) []string {
	var names []string
	for _, e := range rw.Needs(svc) {
		if _, ok := rw.Services[e.Service]; ok {
			names = append(names, e.Service)
		}
	}
	return unionSorted(names)
}

func (rw *ResolvedWorkspace) validateEdges() error {
	names := make([]string, 0, len(rw.Services))
	for name := range rw.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	needs := map[string][]string{}
	for _, name := range names {
		svc := rw.Services[name]
		if svc.Manifest == nil {
			continue
		}
		for _, list := range []struct {
			verb  string
			edges []ServiceEdge
		}{{"needs", svc.Manifest.Needs}, {"calls", svc.Manifest.Calls}} {
			for _, e := range list.edges {
				if _, ok := rw.Services[e.Service]; ok {
					if list.verb == "needs" {
						needs[name] = append(needs[name], e.Service)
					}
					continue
				}
				if !rw.Manifest.Partial {
					return fmt.Errorf("service %q %s the service %q. The workspace does not declare %q", name, list.verb, e.Service, e.Service)
				}
			}
		}
	}
	if cycle := findCycle(names, needs); cycle != nil {
		return fmt.Errorf("these services are in a needs cycle: %s. Change one edge to calls", strings.Join(cycle, " -> "))
	}
	return nil
}

func findCycle(names []string, edges map[string][]string) []string {
	const (
		unseen = iota
		open
		done
	)
	state := map[string]int{}
	var path []string
	var cycle []string
	var visit func(string) bool
	visit = func(n string) bool {
		state[n] = open
		path = append(path, n)
		for _, next := range edges[n] {
			switch state[next] {
			case open:
				for i, p := range path {
					if p == next {
						cycle = append(append([]string(nil), path[i:]...), next)
						return true
					}
				}
			case unseen:
				if visit(next) {
					return true
				}
			}
		}
		path = path[:len(path)-1]
		state[n] = done
		return false
	}
	for _, n := range names {
		if state[n] == unseen && visit(n) {
			return cycle
		}
	}
	return nil
}

func (g *TopologyGraph) NeedsClosure(named []string, exclude map[string]bool) ([]string, map[string][]string) {
	in := map[string]bool{}
	chains := map[string][]string{}
	var queue [][]string
	roots := append([]string(nil), named...)
	sort.Strings(roots)
	for _, n := range roots {
		if exclude[n] || in[n] {
			continue
		}
		in[n] = true
		queue = append(queue, []string{n})
	}
	for len(queue) > 0 {
		chain := queue[0]
		queue = queue[1:]
		svc, ok := g.Services[chain[len(chain)-1]]
		if !ok {
			continue
		}
		for _, dep := range svc.Dependencies {
			if exclude[dep] || in[dep] {
				continue
			}
			in[dep] = true
			next := append(append([]string(nil), chain...), dep)
			chains[dep] = next
			queue = append(queue, next)
		}
	}
	out := make([]string, 0, len(in))
	for n := range in {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, chains
}
