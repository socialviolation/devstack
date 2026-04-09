package observability

import (
	"context"
	"time"
)

// TraceQuery parameters for querying traces from a backend.
type TraceQuery struct {
	TraceID   string        // If set, fetch this specific trace (all other fields ignored)
	SpanID    string        // If set, find the trace containing this span (TraceID takes precedence)
	Service   string        // Optional service filter
	Attribute string        // Optional attribute key (paired with Value)
	Value     string        // Optional attribute value to match
	Since     time.Duration // Lookback window (default: 5 minutes)
	Limit     int           // Max traces to return (default: 3)
}

// LogQuery parameters for querying logs.
type LogQuery struct {
	TraceID string
	Service string
	Since   time.Duration
	Limit   int
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

	// ListServices returns service names that have emitted traces within the given window.
	ListServices(ctx context.Context, since time.Duration) ([]string, error)
}
