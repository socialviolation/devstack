package observability

import (
	"context"
	"testing"
	"time"
)

type recordingBackend struct {
	traces   TraceQuery
	logs     LogQuery
	services ServiceQuery
}

func (r *recordingBackend) QueryTraces(_ context.Context, req TraceQuery) ([][]Span, error) {
	r.traces = req
	return nil, nil
}

func (r *recordingBackend) QueryLogs(_ context.Context, req LogQuery) ([]LogEntry, error) {
	r.logs = req
	return nil, nil
}

func (r *recordingBackend) ListVariants(_ context.Context, req ServiceQuery) ([]ServiceVariant, error) {
	r.services = req
	return nil, nil
}

// A caller that forgets the workspace filter — or deliberately asks for another
// one — must still only see its own workspace's telemetry.
func TestScopedToOverridesWorkspaceOnEveryQuery(t *testing.T) {
	inner := &recordingBackend{}
	backend := ScopedTo(inner, "navexa")
	ctx := context.Background()

	backend.QueryTraces(ctx, TraceQuery{})
	if inner.traces.Workspace != "navexa" {
		t.Errorf("traces workspace = %q", inner.traces.Workspace)
	}

	backend.QueryTraces(ctx, TraceQuery{Workspace: "someone-else"})
	if inner.traces.Workspace != "navexa" {
		t.Errorf("caller-supplied workspace was not overridden: %q", inner.traces.Workspace)
	}

	backend.QueryLogs(ctx, LogQuery{Workspace: "someone-else"})
	if inner.logs.Workspace != "navexa" {
		t.Errorf("logs workspace = %q", inner.logs.Workspace)
	}

	backend.ListVariants(ctx, ServiceQuery{Since: time.Minute})
	if inner.services.Workspace != "navexa" {
		t.Errorf("services workspace = %q", inner.services.Workspace)
	}
	if inner.services.Since != time.Minute {
		t.Errorf("scoping must not disturb other fields: %v", inner.services.Since)
	}
}

func TestScopedToPassesThroughWithoutWorkspace(t *testing.T) {
	inner := &recordingBackend{}
	if got := ScopedTo(inner, ""); got != Backend(inner) {
		t.Error("an empty workspace should not wrap the backend")
	}
	if got := ScopedTo(nil, "navexa"); got != nil {
		t.Error("a nil backend should stay nil")
	}
}
