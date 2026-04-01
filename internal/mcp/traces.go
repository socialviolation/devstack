package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// aspireDashboardURL is the default query URL for the managed Aspire Dashboard.
// The /api/telemetry/* endpoints require Dashboard__Api__Enabled=true and
// Dashboard__Api__AuthMode=Unsecured (set when the container is started by devstack).
const aspireDashboardURL = "http://localhost:18888"

// --- Aspire Dashboard API response types ---
// The /api/telemetry/traces endpoint returns OTLP JSON format.
// See: https://opentelemetry.io/docs/specs/otlp/#json-encoding

type aspireTelemetryResponse struct {
	Data          []aspireResourceSpans `json:"data"`
	TotalCount    int                   `json:"totalCount"`
	ReturnedCount int                   `json:"returnedCount"`
}

// aspireResourceSpans is the OTLP JSON resourceSpans object.
type aspireResourceSpans struct {
	Resource   aspireResource     `json:"resource"`
	ScopeSpans []aspireScopeSpans `json:"scopeSpans"`
}

type aspireResource struct {
	Attributes []aspireKeyValue `json:"attributes"`
}

type aspireScopeSpans struct {
	Scope aspireScope  `json:"scope"`
	Spans []aspireSpan `json:"spans"`
}

type aspireScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type aspireSpan struct {
	TraceID           string           `json:"traceId"`
	SpanID            string           `json:"spanId"`
	ParentSpanID      string           `json:"parentSpanId"`
	Name              string           `json:"name"`
	Kind              int              `json:"kind"`
	StartTimeUnixNano string           `json:"startTimeUnixNano"` // string-encoded nanoseconds
	EndTimeUnixNano   string           `json:"endTimeUnixNano"`   // string-encoded nanoseconds
	Attributes        []aspireKeyValue `json:"attributes"`
	Status            aspireStatus     `json:"status"`
}

type aspireStatus struct {
	Code    int    `json:"code"`    // 0=unset, 1=ok, 2=error
	Message string `json:"message"`
}

type aspireKeyValue struct {
	Key   string      `json:"key"`
	Value aspireValue `json:"value"`
}

type aspireValue struct {
	StringValue string  `json:"stringValue,omitempty"`
	IntValue    string  `json:"intValue,omitempty"` // JSON int64 is string-encoded in OTLP
	BoolValue   bool    `json:"boolValue,omitempty"`
	DoubleValue float64 `json:"doubleValue,omitempty"`
}

// aspireResourcesResponse is returned by /api/telemetry/resources.
type aspireResourcesResponse struct {
	Data []aspireResourceEntry `json:"data"`
}

type aspireResourceEntry struct {
	Name string `json:"name"`
}

// aspireKVString returns the string representation of an aspireValue.
func aspireKVString(v aspireValue) string {
	if v.StringValue != "" {
		return v.StringValue
	}
	if v.IntValue != "" {
		return v.IntValue
	}
	if v.BoolValue {
		return "true"
	}
	if v.DoubleValue != 0 {
		return fmt.Sprintf("%g", v.DoubleValue)
	}
	return ""
}

// aspireAttrValue returns the value for a given attribute key, or "".
func aspireAttrValue(attrs []aspireKeyValue, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return aspireKVString(a.Value)
		}
	}
	return ""
}

// --- Internal unified trace representation ---
// We normalise Aspire Dashboard OTLP JSON into this representation for display.

type traceSpan struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Service      string
	Operation    string
	StartNs      int64 // Unix nanoseconds
	DurationNs   int64
	StatusCode   int // 0=unset, 1=ok, 2=error
	StatusMsg    string
	Attrs        []aspireKeyValue
}

type traceRecord struct {
	TraceID string
	Spans   []traceSpan
}

// --- HTTP helpers ---

func aspireGet(url string, dest interface{}, queryURL string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("observability backend is not reachable at %s — start it with: devstack otel start", queryURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("observability backend returned HTTP %d from %s", resp.StatusCode, url)
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("failed to decode observability response from %s: %w", url, err)
	}
	return nil
}

// fetchTraces fetches traces from the Aspire Dashboard API.
// queryURL: the base URL (e.g. http://localhost:18888).
// service: optional resource name filter.
// limit: max traces to return.
func fetchTraces(queryURL, service string, limit int) ([]traceRecord, error) {
	apiURL := fmt.Sprintf("%s/api/telemetry/traces?limit=%d", queryURL, limit)
	if service != "" {
		apiURL += "&resourceNames=" + service
	}

	var resp aspireTelemetryResponse
	if err := aspireGet(apiURL, &resp, queryURL); err != nil {
		return nil, err
	}

	return normaliseTraces(resp.Data), nil
}

// fetchTrace fetches a single trace by ID from the Aspire Dashboard API.
func fetchTrace(queryURL, traceID string) (*traceRecord, error) {
	apiURL := fmt.Sprintf("%s/api/telemetry/traces/%s", queryURL, traceID)

	var resp aspireTelemetryResponse
	if err := aspireGet(apiURL, &resp, queryURL); err != nil {
		return nil, err
	}

	records := normaliseTraces(resp.Data)
	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}

// fetchResources returns the list of resource names (services) from the Aspire Dashboard.
func fetchResources(queryURL string) ([]string, error) {
	apiURL := fmt.Sprintf("%s/api/telemetry/resources", queryURL)

	var resp aspireResourcesResponse
	if err := aspireGet(apiURL, &resp, queryURL); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resp.Data))
	for _, r := range resp.Data {
		names = append(names, r.Name)
	}
	return names, nil
}

// normaliseTraces converts OTLP JSON resourceSpans into traceRecords grouped by traceID.
func normaliseTraces(resourceSpans []aspireResourceSpans) []traceRecord {
	// traceID -> spans
	traceMap := make(map[string][]traceSpan)

	for _, rs := range resourceSpans {
		svc := aspireAttrValue(rs.Resource.Attributes, "service.name")

		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				startNs := parseNano(s.StartTimeUnixNano)
				endNs := parseNano(s.EndTimeUnixNano)
				durationNs := endNs - startNs
				if durationNs < 0 {
					durationNs = 0
				}

				span := traceSpan{
					TraceID:      s.TraceID,
					SpanID:       s.SpanID,
					ParentSpanID: s.ParentSpanID,
					Service:      svc,
					Operation:    s.Name,
					StartNs:      startNs,
					DurationNs:   durationNs,
					StatusCode:   s.Status.Code,
					StatusMsg:    s.Status.Message,
					Attrs:        s.Attributes,
				}
				traceMap[s.TraceID] = append(traceMap[s.TraceID], span)
			}
		}
	}

	records := make([]traceRecord, 0, len(traceMap))
	for tid, spans := range traceMap {
		records = append(records, traceRecord{TraceID: tid, Spans: spans})
	}

	// Sort by root span start time descending (newest first)
	sort.Slice(records, func(i, j int) bool {
		ri := rootSpan(&records[i])
		rj := rootSpan(&records[j])
		if ri == nil || rj == nil {
			return false
		}
		return ri.StartNs > rj.StartNs
	})

	return records
}

// parseNano parses a nanosecond string (OTLP JSON encodes int64 as string).
func parseNano(s string) int64 {
	if s == "" {
		return 0
	}
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}

// rootSpan finds the root span (no parent in this trace) in a traceRecord.
func rootSpan(r *traceRecord) *traceSpan {
	spanIDs := make(map[string]bool)
	for _, s := range r.Spans {
		spanIDs[s.SpanID] = true
	}
	for i := range r.Spans {
		s := &r.Spans[i]
		if s.ParentSpanID == "" || !spanIDs[s.ParentSpanID] {
			return s
		}
	}
	if len(r.Spans) > 0 {
		return &r.Spans[0]
	}
	return nil
}

// spanHasError returns true if a span has OTLP status code 2 (ERROR).
func spanHasError(s *traceSpan) bool {
	return s.StatusCode == 2
}

// --- Formatters ---

// formatTraceList formats a slice of traceRecords as a human-readable table.
func formatTraceList(traces []traceRecord) string {
	if len(traces) == 0 {
		return "No traces found.\n"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-19s %-20s %-26s %-12s %-8s %s\n", "TIME", "TRACE_ID", "OPERATION", "SERVICE", "MS", "STATUS")
	fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 100))
	for _, trace := range traces {
		root := rootSpan(&trace)
		if root == nil {
			continue
		}
		ts := time.Unix(0, root.StartNs).Local().Format("01-02 15:04:05")
		traceID := trace.TraceID
		if len(traceID) > 18 {
			traceID = traceID[:16] + ".."
		}
		op := root.Operation
		if len(op) > 24 {
			op = op[:21] + "..."
		}
		svc := root.Service
		if len(svc) > 10 {
			svc = svc[:10]
		}
		durationMs := float64(root.DurationNs) / 1e6
		status := "ok"
		if spanHasError(root) {
			status = "error"
		}
		fmt.Fprintf(&sb, "%-19s %-20s %-26s %-12s %-8.1f %s\n", ts, traceID, op, svc, durationMs, status)
	}
	return sb.String()
}

// formatSpanTree formats the full span tree for a single traceRecord.
func formatSpanTree(r *traceRecord) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Trace: %s\n\n", r.TraceID)

	if len(r.Spans) == 0 {
		sb.WriteString("No spans found.\n")
		return sb.String()
	}

	// Build parent map: spanID -> parentSpanID
	parentMap := make(map[string]string)
	for _, sp := range r.Spans {
		if sp.ParentSpanID != "" {
			parentMap[sp.SpanID] = sp.ParentSpanID
		}
	}

	// Sort spans by start time ascending
	spans := make([]traceSpan, len(r.Spans))
	copy(spans, r.Spans)
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].StartNs < spans[j].StartNs
	})

	// Business attribute keys to display
	businessKeys := map[string]bool{
		"portfolio.id": true,
		"user.id":      true,
		"process.id":   true,
		"file.type":    true,
		"provider.id":  true,
		"trade.count":  true,
		"batch.number": true,
	}
	httpKeys := map[string]bool{
		"http.method":      true,
		"http.route":       true,
		"http.status_code": true,
	}

	for _, sp := range spans {
		durationMs := float64(sp.DurationNs) / 1e6

		// Compute depth
		depth := 0
		cur := sp.SpanID
		for {
			parent, ok := parentMap[cur]
			if !ok {
				break
			}
			depth++
			cur = parent
			if depth > 20 {
				break
			}
		}
		indent := strings.Repeat("  ", depth)

		status := "ok"
		if spanHasError(&sp) {
			status = "ERROR"
		}

		fmt.Fprintf(&sb, "%s[%s] %s  %.1fms  [%s]\n", indent, sp.Service, sp.Operation, durationMs, status)

		for _, attr := range sp.Attrs {
			if businessKeys[attr.Key] {
				fmt.Fprintf(&sb, "%s  %s: %s\n", indent, attr.Key, aspireKVString(attr.Value))
			}
		}
		for _, attr := range sp.Attrs {
			if httpKeys[attr.Key] {
				fmt.Fprintf(&sb, "%s  %s: %s\n", indent, attr.Key, aspireKVString(attr.Value))
			}
		}
		if status == "ERROR" {
			for _, attr := range sp.Attrs {
				if attr.Key == "error.message" || attr.Key == "exception.message" || attr.Key == "exception.type" {
					fmt.Fprintf(&sb, "%s  %s: %s\n", indent, attr.Key, aspireKVString(attr.Value))
				}
			}
			if sp.StatusMsg != "" {
				fmt.Fprintf(&sb, "%s  status.message: %s\n", indent, sp.StatusMsg)
			}
		}
	}
	return sb.String()
}
