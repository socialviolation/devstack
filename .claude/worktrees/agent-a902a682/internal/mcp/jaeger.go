package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const jaegerQueryURL = "http://localhost:16686"

// --- Jaeger API response types ---

type jaegerResponse struct {
	Data   []jaegerTrace `json:"data"`
	Errors interface{}   `json:"errors"`
}

type jaegerServicesResponse struct {
	Data   []string    `json:"data"`
	Errors interface{} `json:"errors"`
}

type jaegerTrace struct {
	TraceID   string                   `json:"traceID"`
	Spans     []jaegerSpan             `json:"spans"`
	Processes map[string]jaegerProcess `json:"processes"`
}

type jaegerSpan struct {
	TraceID       string       `json:"traceID"`
	SpanID        string       `json:"spanID"`
	OperationName string       `json:"operationName"`
	StartTime     int64        `json:"startTime"` // microseconds since epoch
	Duration      int64        `json:"duration"`  // microseconds
	Tags          []jaegerTag  `json:"tags"`
	ProcessID     string       `json:"processID"`
	References    []jaegerRef  `json:"references"`
}

type jaegerTag struct {
	Key   string      `json:"key"`
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

type jaegerProcess struct {
	ServiceName string      `json:"serviceName"`
	Tags        []jaegerTag `json:"tags"`
}

type jaegerRef struct {
	RefType string `json:"refType"`
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

// --- HTTP helpers ---

func jaegerGet(apiURL string, dest interface{}) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return fmt.Errorf("Jaeger is not reachable — start it with: devstack otel start")
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("failed to decode Jaeger response: %w", err)
	}
	return nil
}

func jaegerGetTraces(params string) (*jaegerResponse, error) {
	url := fmt.Sprintf("%s/api/traces?%s", jaegerQueryURL, params)
	var result jaegerResponse
	return &result, jaegerGet(url, &result)
}

func jaegerGetServices() ([]string, error) {
	url := fmt.Sprintf("%s/api/services", jaegerQueryURL)
	var result jaegerServicesResponse
	if err := jaegerGet(url, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

// --- Span helpers ---

// spanTagValue returns the value for a tag key as a string, or "" if not found.
func spanTagValue(span *jaegerSpan, key string) string {
	for _, t := range span.Tags {
		if t.Key == key {
			return fmt.Sprintf("%v", t.Value)
		}
	}
	return ""
}

// spanHasError returns true if the span has error=true or status ERROR.
func spanHasError(span *jaegerSpan) bool {
	for _, t := range span.Tags {
		switch t.Key {
		case "error":
			switch v := t.Value.(type) {
			case bool:
				if v {
					return true
				}
			case string:
				if v == "true" || v == "1" {
					return true
				}
			}
		case "otel.status_code":
			if s, ok := t.Value.(string); ok && s == "ERROR" {
				return true
			}
		}
	}
	return false
}

// findRootSpan returns the root span (no CHILD_OF reference).
func findRootSpan(trace *jaegerTrace) *jaegerSpan {
	for i := range trace.Spans {
		sp := &trace.Spans[i]
		isRoot := true
		for _, ref := range sp.References {
			if ref.RefType == "CHILD_OF" {
				isRoot = false
				break
			}
		}
		if isRoot {
			return sp
		}
	}
	if len(trace.Spans) > 0 {
		return &trace.Spans[0]
	}
	return nil
}

// --- Formatters ---

// formatTraceList formats a slice of traces as a human-readable table.
func formatTraceList(traces []jaegerTrace) string {
	if len(traces) == 0 {
		return "No traces found.\n"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-19s %-20s %-26s %-10s %-8s %s\n", "TIME", "TRACE_ID", "OPERATION", "SERVICE", "MS", "STATUS")
	fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 100))
	for _, trace := range traces {
		root := findRootSpan(&trace)
		if root == nil {
			continue
		}
		ts := time.UnixMicro(root.StartTime).Local().Format("01-02 15:04:05")
		traceID := trace.TraceID
		if len(traceID) > 18 {
			traceID = traceID[:16] + ".."
		}
		op := root.OperationName
		if len(op) > 24 {
			op = op[:21] + "..."
		}
		svc := ""
		if proc, ok := trace.Processes[root.ProcessID]; ok {
			svc = proc.ServiceName
		}
		if len(svc) > 8 {
			svc = svc[:8]
		}
		durationMs := float64(root.Duration) / 1000.0
		status := "ok"
		if spanHasError(root) {
			status = "error"
		}
		fmt.Fprintf(&sb, "%-19s %-20s %-26s %-10s %-8.1f %s\n", ts, traceID, op, svc, durationMs, status)
	}
	return sb.String()
}

// formatSpanTree formats the full span tree for a single trace.
func formatSpanTree(trace *jaegerTrace) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Trace: %s\n\n", trace.TraceID)

	if len(trace.Spans) == 0 {
		sb.WriteString("No spans found.\n")
		return sb.String()
	}

	// Build parent map: spanID -> parentSpanID
	parentMap := make(map[string]string)
	for _, sp := range trace.Spans {
		for _, ref := range sp.References {
			if ref.RefType == "CHILD_OF" {
				parentMap[sp.SpanID] = ref.SpanID
				break
			}
		}
	}

	// Sort spans by start time
	spans := make([]jaegerSpan, len(trace.Spans))
	copy(spans, trace.Spans)
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].StartTime < spans[j].StartTime
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
		svc := ""
		if proc, ok := trace.Processes[sp.ProcessID]; ok {
			svc = proc.ServiceName
		}
		durationMs := float64(sp.Duration) / 1000.0

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

		spanIDShort := sp.SpanID
		if len(spanIDShort) > 8 {
			spanIDShort = spanIDShort[:8]
		}

		fmt.Fprintf(&sb, "%s[%s] %s  %.1fms  [%s]\n", indent, svc, sp.OperationName, durationMs, status)

		// Print business tags
		for _, tag := range sp.Tags {
			if businessKeys[tag.Key] {
				fmt.Fprintf(&sb, "%s  %s: %v\n", indent, tag.Key, tag.Value)
			}
		}
		// Print HTTP tags if present
		for _, tag := range sp.Tags {
			if httpKeys[tag.Key] {
				fmt.Fprintf(&sb, "%s  %s: %v\n", indent, tag.Key, tag.Value)
			}
		}
		// Print error details
		if status == "ERROR" {
			for _, tag := range sp.Tags {
				if tag.Key == "error.message" || tag.Key == "exception.message" || tag.Key == "exception.type" {
					fmt.Fprintf(&sb, "%s  %s: %v\n", indent, tag.Key, tag.Value)
				}
			}
			_ = spanIDShort
		}
	}
	return sb.String()
}

// timeWindowParams returns Jaeger start/end query params for a lookback window.
func timeWindowParams(sinceMinutes int) string {
	now := time.Now()
	end := now.UnixMicro()
	start := now.Add(-time.Duration(sinceMinutes) * time.Minute).UnixMicro()
	return fmt.Sprintf("start=%d&end=%d", start, end)
}
