package observability

import (
	"fmt"
)

// NewBackendFunc is a factory function type for creating backends.
// Used to avoid import cycles — the actual factory lives in cmd/serve.go
// or can be registered via RegisterBackend.
type NewBackendFunc func(url, apiKey string) Backend

var registeredBackends = map[string]NewBackendFunc{}

// RegisterBackend registers a backend constructor by name.
// Call this from an init() in the backend implementation package.
func RegisterBackend(name string, fn NewBackendFunc) {
	registeredBackends[name] = fn
}

// NewBackend creates a Backend from config values.
// Backends must be registered via RegisterBackend (typically via init() in their package).
// Currently only "signoz" is supported. Empty backend defaults to "signoz".
func NewBackend(backend, url, apiKey string) (Backend, error) {
	if backend == "" {
		backend = "signoz"
	}
	fn, ok := registeredBackends[backend]
	if !ok {
		return nil, fmt.Errorf("unknown observability backend %q (supported: %v)", backend, registeredBackendNames())
	}
	return fn(url, apiKey), nil
}

func registeredBackendNames() []string {
	names := make([]string, 0, len(registeredBackends))
	for k := range registeredBackends {
		names = append(names, k)
	}
	return names
}
