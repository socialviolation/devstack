package hostdaemon

import "strings"

// Scope names the resources a write may change, so a command that acts on some
// services leaves every other resource at the block already on disk.
type Scope struct {
	All    bool
	Names  map[string]bool
	Stacks map[string]bool
}

// ScopeAll covers every resource.
func ScopeAll() Scope {
	return Scope{All: true}
}

// ScopeNames covers the given resource names, as resourceName builds them.
func ScopeNames(names ...string) Scope {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return Scope{Names: set}
}

// ScopeStack covers every resource of one stack in one workspace. An empty
// stack name covers that workspace's base services.
func ScopeStack(workspace, stack string) Scope {
	return Scope{Stacks: map[string]bool{workspace + ":" + stack: true}}
}

// Covers reports whether the scope may write this resource's block.
func (s Scope) Covers(resource string) bool {
	if s.All {
		return true
	}
	if s.Names[resource] {
		return true
	}
	parts := strings.Split(resource, ":")
	if len(parts) < 2 {
		return false
	}
	ns := parts[0] + ":"
	if len(parts) > 2 {
		ns += parts[2]
	}
	return s.Stacks[ns]
}
