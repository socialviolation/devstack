package observability

import "context"

// ScopedTo confines every query to one workspace's telemetry. One backend now
// holds every workspace on the machine, so an unscoped query would answer with
// an unrelated project's traces — this makes that impossible to do by accident
// rather than leaving each call site to remember the filter.
func ScopedTo(backend Backend, workspace string) Backend {
	if backend == nil || workspace == "" {
		return backend
	}
	return &scopedBackend{inner: backend, workspace: workspace}
}

type scopedBackend struct {
	inner     Backend
	workspace string
}

func (s *scopedBackend) QueryTraces(ctx context.Context, req TraceQuery) ([][]Span, error) {
	req.Workspace = s.workspace
	return s.inner.QueryTraces(ctx, req)
}

func (s *scopedBackend) QueryLogs(ctx context.Context, req LogQuery) ([]LogEntry, error) {
	req.Workspace = s.workspace
	return s.inner.QueryLogs(ctx, req)
}

func (s *scopedBackend) ListVariants(ctx context.Context, req ServiceQuery) ([]ServiceVariant, error) {
	req.Workspace = s.workspace
	return s.inner.ListVariants(ctx, req)
}
