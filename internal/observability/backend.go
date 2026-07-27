package observability

import (
	"context"
	"time"
)

// TraceQuery parameters for querying traces from a backend.
type TraceQuery struct {
	TraceID   string        // If set, fetch this specific trace (all other fields ignored)
	SpanID    string        // If set, find the trace containing this span (TraceID takes precedence)
	Workspace string        // devstack.workspace filter; set for you by ScopedTo
	Service   string        // Optional service filter
	Stack     string        // Optional devstack.stack resource-attribute filter ("base" for base workspace)
	Attribute string        // Optional attribute key (paired with Value)
	Value     string        // Optional attribute value to match
	Since     time.Duration // Lookback window (default: 5 minutes)
	Limit     int           // Max traces to return (default: 3)
}

// LogQuery parameters for querying logs.
type LogQuery struct {
	TraceID   string
	Workspace string // devstack.workspace filter; set for you by ScopedTo
	Service   string
	Since     time.Duration
	Limit     int
}

// ServiceVariant is one distinguishable instance of a service. The same service
// runs many times over — in the base workspace, in each feature stack, under
// each config env — and all of them report to the one shared backend, so these
// fields are what tell a caller which one they are looking at.
type ServiceVariant struct {
	// Service is the name the service reports itself as (OTEL service.name).
	Service string
	// Devstack is the name devstack knows it by, which is what a caller stands
	// in and filters on. It often differs from Service.
	Devstack string
	// Stack is the feature stack ("base" for the base workspace).
	Stack string
	// Env is the config env the variant runs under, when one is selected.
	Env string
	// Spans is how many spans this variant reported in the window — the evidence
	// that it is actually emitting, not merely configured to.
	Spans int
}

// ServiceQuery parameters for listing services that reported telemetry.
type ServiceQuery struct {
	Workspace string // devstack.workspace filter; set for you by ScopedTo
	Since     time.Duration
}

// Span represents a single span in a distributed trace.
type Span struct {
	SpanID       string
	TraceID      string
	ParentSpanID string
	Service      string
	Operation    string
	DurationNano int64
	Status       string // e.g. "STATUS_CODE_OK", "STATUS_CODE_ERROR", "ok", "error"
	Attributes   map[string]string
	StartTime    time.Time
}

// LogEntry represents a single log line correlated with a trace.
type LogEntry struct {
	Timestamp time.Time
	Body      string
	Service   string
	TraceID   string
	SpanID    string
	Severity  string
}

// Backend is the interface all observability backends must implement.
// Implementations: signoz (internal/observability/signoz/)
type Backend interface {
	// QueryTraces returns matching traces. Each trace is a []Span (all spans in that trace).
	// If req.TraceID is set, returns a single trace. Otherwise searches by filters.
	QueryTraces(ctx context.Context, req TraceQuery) ([][]Span, error)

	// QueryLogs returns log entries matching the query.
	QueryLogs(ctx context.Context, req LogQuery) ([]LogEntry, error)

	// ListVariants returns the distinct service variants that reported telemetry
	// in the window, so a caller can see which instance to query.
	ListVariants(ctx context.Context, req ServiceQuery) ([]ServiceVariant, error)
}
