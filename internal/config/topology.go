package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type TopologyIssueSeverity string

const (
	TopologyIssueError   TopologyIssueSeverity = "error"
	TopologyIssueWarning TopologyIssueSeverity = "warning"
)

type TopologyIssue struct {
	Severity TopologyIssueSeverity
	Code     string
	Message  string
}

type ServiceTopology struct {
	Name         string
	Path         string
	Groups       []string
	Dependencies []string
	Dependents   []string
	Calls        []string
	CalledBy     []string
	Source       string
}

type TopologyGraph struct {
	WorkspaceRoot string
	WorkspaceName string
	Services      map[string]*ServiceTopology
	Groups        map[string][]string
	Issues        []TopologyIssue
}

func BuildTopology(workspacePath string) (*TopologyGraph, error) {
	resolved, err := ResolveWorkspace(workspacePath)
	if err != nil {
		return nil, err
	}

	graph := &TopologyGraph{
		WorkspaceRoot: filepath.Clean(workspacePath),
		WorkspaceName: resolved.Manifest.Workspace.Name,
		Services:      map[string]*ServiceTopology{},
		Groups:        cloneStringSlicesMap(resolved.Manifest.Groups),
	}

	for name, service := range resolved.Services {
		graph.Services[name] = &ServiceTopology{
			Name:         name,
			Path:         service.RepoPath,
			Dependencies: resolved.Manifest.ResourceDeps(name),
			Calls:        unionSorted(resolved.Manifest.Calls[name]),
			Source:       service.Source,
		}
	}

	for group, members := range graph.Groups {
		sort.Strings(members)
		graph.Groups[group] = members
		for _, member := range members {
			service, ok := graph.Services[member]
			if !ok {
				graph.Issues = append(graph.Issues, TopologyIssue{
					Severity: TopologyIssueError,
					Code:     "missing-group-member",
					Message:  fmt.Sprintf("group %q references the unknown service %q", group, member),
				})
				continue
			}
			service.Groups = append(service.Groups, group)
		}
	}

	for name, service := range graph.Services {
		sort.Strings(service.Groups)
		sort.Strings(service.Dependencies)
		for _, dep := range service.Dependencies {
			depService, ok := graph.Services[dep]
			if !ok {
				graph.Issues = append(graph.Issues, TopologyIssue{
					Severity: TopologyIssueError,
					Code:     "missing-dependency",
					Message:  fmt.Sprintf("service %q depends on the unknown service %q", name, dep),
				})
				continue
			}
			depService.Dependents = append(depService.Dependents, name)
		}
		for _, callee := range service.Calls {
			calleeService, ok := graph.Services[callee]
			if !ok {
				continue
			}
			calleeService.CalledBy = append(calleeService.CalledBy, name)
		}
	}

	for _, service := range graph.Services {
		sort.Strings(service.Dependents)
		sort.Strings(service.CalledBy)
	}

	graph.Issues = append(graph.Issues, detectTopologyCycles(graph.Services)...)
	sort.Slice(graph.Issues, func(i, j int) bool {
		if graph.Issues[i].Severity != graph.Issues[j].Severity {
			return graph.Issues[i].Severity < graph.Issues[j].Severity
		}
		if graph.Issues[i].Code != graph.Issues[j].Code {
			return graph.Issues[i].Code < graph.Issues[j].Code
		}
		return graph.Issues[i].Message < graph.Issues[j].Message
	})

	return graph, nil
}

func (g *TopologyGraph) ServiceNames() []string {
	names := make([]string, 0, len(g.Services))
	for name := range g.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (g *TopologyGraph) GroupNames() []string {
	names := make([]string, 0, len(g.Groups))
	for name := range g.Groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (g *TopologyGraph) HasErrors() bool {
	for _, issue := range g.Issues {
		if issue.Severity == TopologyIssueError {
			return true
		}
	}
	return false
}

// TransitiveCallers returns the set of services that directly or transitively
// call name, walking CalledBy edges only (start-order edges are excluded, so a
// shared start-order dependency does not inflate the closure). The result is
// sorted and normally excludes name itself; a cyclic call graph can include it
// because it genuinely calls itself transitively. Visited-set guarded, so it
// terminates on call cycles even though tiltgen never validates them.
func (g *TopologyGraph) TransitiveCallers(name string) []string {
	result := map[string]bool{}
	visited := map[string]bool{}

	var visit func(string)
	visit = func(n string) {
		service, ok := g.Services[n]
		if !ok {
			return
		}
		for _, caller := range service.CalledBy {
			result[caller] = true
			if !visited[caller] {
				visited[caller] = true
				visit(caller)
			}
		}
	}
	visit(name)

	callers := make([]string, 0, len(result))
	for caller := range result {
		callers = append(callers, caller)
	}
	sort.Strings(callers)
	return callers
}

// TransitiveNeeds returns the set of services that name directly or transitively
// needs: the services it starts after, and the services it calls. A stack
// overlays these so it stands up its own copy of what it needs, instead of
// reaching into base. The result is sorted and normally excludes name itself; a
// cycle can include it. Visited-set guarded, so it terminates on a cycle that
// detectTopologyCycles reports but does not remove.
func (g *TopologyGraph) TransitiveNeeds(name string) []string {
	result := map[string]bool{}
	visited := map[string]bool{}

	var visit func(string)
	visit = func(n string) {
		service, ok := g.Services[n]
		if !ok {
			return
		}
		for _, need := range append(append([]string(nil), service.Dependencies...), service.Calls...) {
			result[need] = true
			if !visited[need] {
				visited[need] = true
				visit(need)
			}
		}
	}
	visit(name)

	needs := make([]string, 0, len(result))
	for need := range result {
		needs = append(needs, need)
	}
	sort.Strings(needs)
	return needs
}

func detectTopologyCycles(services map[string]*ServiceTopology) []TopologyIssue {
	var issues []TopologyIssue
	visited := map[string]bool{}
	stack := map[string]bool{}
	path := []string{}
	seenCycles := map[string]bool{}

	var visit func(string)
	visit = func(name string) {
		if stack[name] {
			idx := 0
			for i, item := range path {
				if item == name {
					idx = i
					break
				}
			}
			cycle := append(append([]string(nil), path[idx:]...), name)
			key := strings.Join(cycle, "->")
			if !seenCycles[key] {
				seenCycles[key] = true
				issues = append(issues, TopologyIssue{
					Severity: TopologyIssueError,
					Code:     "dependency-cycle",
					Message:  fmt.Sprintf("these services are in a dependency cycle: %s", strings.Join(cycle, " -> ")),
				})
			}
			return
		}
		if visited[name] {
			return
		}
		visited[name] = true
		stack[name] = true
		path = append(path, name)
		for _, dep := range services[name].Dependencies {
			if _, ok := services[dep]; !ok {
				continue
			}
			visit(dep)
		}
		path = path[:len(path)-1]
		delete(stack, name)
	}

	for name := range services {
		visit(name)
	}
	return issues
}
