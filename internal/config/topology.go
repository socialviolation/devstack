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
	Via          map[string]string
	Source       string
	overlayCalls []string
	overlayBy    []string
}

func (s *ServiceTopology) WithVia(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if v := s.Via[n]; v != "" {
			out[i] = n + " (" + v + ")"
		}
	}
	return out
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
		node := &ServiceTopology{
			Name:   name,
			Path:   service.RepoPath,
			Via:    map[string]string{},
			Source: service.Source,
		}
		for _, e := range resolved.Needs(name) {
			node.Dependencies = append(node.Dependencies, e.Service)
			if e.Via != "" {
				node.Via[e.Service] = e.Via
			}
		}
		for _, e := range resolved.CallEdges(name) {
			node.Calls = append(node.Calls, e.Service)
			if e.Via != "" {
				node.Via[e.Service] = e.Via
			}
		}
		node.Dependencies = unionSorted(node.Dependencies)
		if service.Manifest != nil {
			for _, e := range service.Manifest.Calls {
				node.overlayCalls = append(node.overlayCalls, e.Service)
			}
		}
		node.overlayCalls = unionSorted(node.overlayCalls, resolved.Manifest.Calls[name])
		graph.Services[name] = node
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
					Message:  fmt.Sprintf("service %q needs the unknown service %q", name, dep),
				})
				continue
			}
			depService.Dependents = append(depService.Dependents, name)
		}
		for _, callee := range service.Calls {
			calleeService, ok := graph.Services[callee]
			if !ok {
				graph.Issues = append(graph.Issues, TopologyIssue{
					Severity: TopologyIssueError,
					Code:     "missing-call",
					Message:  fmt.Sprintf("service %q calls the unknown service %q", name, callee),
				})
				continue
			}
			calleeService.CalledBy = append(calleeService.CalledBy, name)
		}
		for _, callee := range service.overlayCalls {
			if calleeService, ok := graph.Services[callee]; ok {
				calleeService.overlayBy = append(calleeService.overlayBy, name)
			}
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

// TransitiveCallers walks only the calls declared as calls. The legacy
// dependencies and startsAfter maps now load as calls too, and walking them
// would pull every dependent into each stack overlay.
func (g *TopologyGraph) TransitiveCallers(name string) []string {
	result := map[string]bool{}
	visited := map[string]bool{}

	var visit func(string)
	visit = func(n string) {
		service, ok := g.Services[n]
		if !ok {
			return
		}
		for _, caller := range service.overlayBy {
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
