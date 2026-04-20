package otel

import "sort"

var plugins = map[string]Plugin{}

// Register adds a plugin to the global registry.
// Typically called from plugin init() functions.
func Register(p Plugin) {
	plugins[p.Name()] = p
}

// Get returns the plugin with the given name.
// Falls back to "signoz" if name is unknown or empty.
func Get(name string) Plugin {
	if name != "" {
		if p, ok := plugins[name]; ok {
			return p
		}
	}
	// Fall back to signoz
	if p, ok := plugins["signoz"]; ok {
		return p
	}
	// Last resort: return first registered plugin
	for _, p := range plugins {
		return p
	}
	return nil
}

// All returns all registered plugins sorted by name.
func All() []Plugin {
	result := make([]Plugin, 0, len(plugins))
	for _, p := range plugins {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result
}
