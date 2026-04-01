package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// signozQueryURL is the default query URL for the managed SigNoz query-service.
const signozQueryURL = "http://localhost:8080"

// --- SigNoz query_range API types ---

// signozQueryRangeRequest is the body for POST /api/v3/query_range.
type signozQueryRangeRequest struct {
	Start          int64                  `json:"start"`
	End            int64                  `json:"end"`
	Step           int                    `json:"step"`
	Variables      map[string]interface{} `json:"variables"`
	CompositeQuery signozCompositeQuery   `json:"compositeQuery"`
}

type signozCompositeQuery struct {
	QueryType      string                        `json:"queryType"`
	PanelType      string                        `json:"panelType"`
	BuilderQueries map[string]signozBuilderQuery `json:"builderQueries"`
}

type signozBuilderQuery struct {
	DataSource         string                 `json:"dataSource"`
	QueryName          string                 `json:"queryName"`
	Expression         string                 `json:"expression"`
	AggregateOperator  string                 `json:"aggregateOperator"`
	AggregateAttribute map[string]interface{} `json:"aggregateAttribute"`
	Filters            signozFilters          `json:"filters"`
	OrderBy            []signozOrderBy        `json:"orderBy"`
	Limit              int                    `json:"limit"`
	PageSize           int                    `json:"pageSize"`
	SelectColumns      []signozFilterKey      `json:"selectColumns"`
}

type signozFilters struct {
	Op    string         `json:"op"`
	Items []signozFilter `json:"items"`
}

type signozFilter struct {
	Key   signozFilterKey `json:"key"`
	Op    string          `json:"op"`
	Value interface{}     `json:"value"`
}

type signozFilterKey struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	DataType string `json:"dataType"`
	IsColumn bool   `json:"isColumn"`
}

type signozOrderBy struct {
	ColumnName string `json:"columnName"`
	Order      string `json:"order"`
}

// signozQueryRangeResponse is the response from POST /api/v3/query_range.
type signozQueryRangeResponse struct {
	Status string               `json:"status"`
	Data   signozQueryRangeData `json:"data"`
}

type signozQueryRangeData struct {
	ResultType string              `json:"resultType"`
	Result     []signozQueryResult `json:"result"`
}

type signozQueryResult struct {
	QueryName string          `json:"queryName"`
	Series    interface{}     `json:"series"` // unused for list panel
	List      []signozListRow `json:"list"`
}

// signozListRow is a single row in a list panel result.
type signozListRow struct {
	Timestamp string                 `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}

// --- SigNoz trace detail API types ---

// signozTraceResponse is the response from GET /api/v1/traces/{traceID}.
type signozTraceResponse struct {
	Status string            `json:"status"`
	Data   []signozSpanEntry `json:"data"`
}

type signozSpanEntry struct {
	TraceID       string                 `json:"traceID"`
	SpanID        string                 `json:"spanID"`
	ParentSpanID  string                 `json:"parentSpanID"`
	Name          string                 `json:"name"`
	ServiceName   string                 `json:"serviceName"`
	DurationNano  int64                  `json:"durationNano"`
	TimeUnixNano  int64                  `json:"timeUnixNano"`
	StatusCode    string                 `json:"statusCode"`
	StatusMessage string                 `json:"statusMessage"`
	Tags          []signozTag            `json:"tags"`
	Attributes    map[string]interface{} `json:"attributes"`
}

type signozTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

// --- Internal unified trace representation ---

type traceSpan struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Service      string
	Operation    string
	StartNs      int64  // Unix nanoseconds
	DurationNs   int64
	StatusCode   string // "ok", "error", "unset"
	StatusMsg    string
	Attrs        map[string]string
}

type traceRecord struct {
	TraceID string
	Spans   []traceSpan
}

// --- HTTP helpers ---

func signozGet(url string, dest interface{}, queryURL string) error {
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

func signozPost(url string, body interface{}, dest interface{}, queryURL string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
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

// selectColumnsFor returns the required selectColumns for a given dataSource in list panel queries.
func selectColumnsFor(dataSource string) []signozFilterKey {
	col := func(key, dataType string) signozFilterKey {
		return signozFilterKey{Key: key, Type: "tag", DataType: dataType, IsColumn: true}
	}
	switch dataSource {
	case "logs":
		return []signozFilterKey{
			col("timestamp", "string"),
			col("body", "string"),
			col("severityText", "string"),
			col("traceID", "string"),
			col("spanID", "string"),
		}
	default: // "traces"
		return []signozFilterKey{
			col("serviceName", "string"),
			col("name", "string"),
			col("traceID", "string"),
			col("spanID", "string"),
			col("parentSpanID", "string"),
			col("durationNano", "float64"),
			col("statusCode", "string"),
			col("timestamp", "string"),
		}
	}
}

// buildQueryRangeRequest builds a POST /api/v3/query_range request body.
// dataSource is "traces" or "logs".
func buildQueryRangeRequest(dataSource, service string, limit int, sinceMinutes int, extraFilters []signozFilter) signozQueryRangeRequest {
	now := time.Now()
	endMs := now.UnixMilli()
	startMs := now.Add(-time.Duration(sinceMinutes) * time.Minute).UnixMilli()

	filters := signozFilters{
		Op:    "AND",
		Items: extraFilters,
	}
	if filters.Items == nil {
		filters.Items = []signozFilter{}
	}

	if service != "" {
		filters.Items = append(filters.Items, signozFilter{
			Key: signozFilterKey{
				Key:      "serviceName",
				Type:     "tag",
				DataType: "string",
				IsColumn: true,
			},
			Op:    "=",
			Value: service,
		})
	}

	return signozQueryRangeRequest{
		Start:     startMs,
		End:       endMs,
		Step:      60,
		Variables: map[string]interface{}{},
		CompositeQuery: signozCompositeQuery{
			QueryType: "builder",
			PanelType: "list",
			BuilderQueries: map[string]signozBuilderQuery{
				"A": {
					DataSource:         dataSource,
					QueryName:          "A",
					Expression:         "A",
					AggregateOperator:  "noop",
					AggregateAttribute: map[string]interface{}{},
					Filters:            filters,
					OrderBy: []signozOrderBy{
						{ColumnName: "timestamp", Order: "desc"},
					},
					Limit:         limit,
					PageSize:      limit,
					SelectColumns: selectColumnsFor(dataSource),
				},
			},
		},
	}
}

// extractListRows returns the list rows from query A in the response.
func extractListRows(resp signozQueryRangeResponse) []signozListRow {
	for _, result := range resp.Data.Result {
		if result.QueryName == "A" {
			return result.List
		}
	}
	return nil
}

// rowToTraceSpan converts a signozListRow to a traceSpan.
func rowToTraceSpan(row signozListRow) traceSpan {
	d := row.Data

	getString := func(key string) string {
		if v, ok := d[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	getInt64 := func(key string) int64 {
		if v, ok := d[key]; ok {
			switch n := v.(type) {
			case float64:
				return int64(n)
			case int64:
				return n
			}
		}
		return 0
	}

	// SigNoz stores timestamps in nanoseconds in the data map.
	startNs := getInt64("timestamp")
	if startNs == 0 {
		// Fall back to row.Timestamp RFC3339 string.
		if t, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil {
			startNs = t.UnixNano()
		}
	}

	durationNs := getInt64("durationNano")

	attrs := make(map[string]string)
	for k, v := range d {
		if s, ok := v.(string); ok {
			attrs[k] = s
		}
	}

	statusCode := getString("statusCode")
	if statusCode == "" {
		statusCode = "unset"
	}

	return traceSpan{
		TraceID:      getString("traceID"),
		SpanID:       getString("spanID"),
		ParentSpanID: getString("parentSpanID"),
		Service:      getString("serviceName"),
		Operation:    getString("name"),
		StartNs:      startNs,
		DurationNs:   durationNs,
		StatusCode:   statusCode,
		StatusMsg:    getString("statusMessage"),
		Attrs:        attrs,
	}
}

// groupSpansByTrace groups a flat list of spans into traceRecords sorted by newest first.
func groupSpansByTrace(spans []traceSpan) []traceRecord {
	traceMap := make(map[string][]traceSpan)
	for _, s := range spans {
		if s.TraceID == "" {
			continue
		}
		traceMap[s.TraceID] = append(traceMap[s.TraceID], s)
	}

	records := make([]traceRecord, 0, len(traceMap))
	for tid, ss := range traceMap {
		records = append(records, traceRecord{TraceID: tid, Spans: ss})
	}

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

// fetchTraces fetches recent traces from the SigNoz query-service.
// queryURL: base URL (e.g. http://localhost:8080).
// service: optional service name filter.
// limit: max traces to return.
// sinceMinutes: look-back window.
func fetchTraces(queryURL, service string, limit int, sinceMinutes int) ([]traceRecord, error) {
	apiURL := fmt.Sprintf("%s/api/v3/query_range", queryURL)
	req := buildQueryRangeRequest("traces", service, limit, sinceMinutes, nil)

	var resp signozQueryRangeResponse
	if err := signozPost(apiURL, req, &resp, queryURL); err != nil {
		return nil, err
	}

	rows := extractListRows(resp)
	spans := make([]traceSpan, 0, len(rows))
	for _, row := range rows {
		spans = append(spans, rowToTraceSpan(row))
	}

	return groupSpansByTrace(spans), nil
}

// fetchTrace fetches a single trace by ID from the SigNoz query-service.
func fetchTrace(queryURL, traceID string) (*traceRecord, error) {
	apiURL := fmt.Sprintf("%s/api/v1/traces/%s", queryURL, traceID)

	var resp signozTraceResponse
	if err := signozGet(apiURL, &resp, queryURL); err != nil {
		return nil, err
	}

	if len(resp.Data) == 0 {
		return nil, nil
	}

	spans := make([]traceSpan, 0, len(resp.Data))
	for _, entry := range resp.Data {
		attrs := make(map[string]string)
		for _, tag := range entry.Tags {
			attrs[tag.Key] = tag.Value
		}
		for k, v := range entry.Attributes {
			if s, ok := v.(string); ok {
				attrs[k] = s
			}
		}

		statusCode := strings.ToLower(entry.StatusCode)
		if statusCode == "" {
			statusCode = "unset"
		}

		spans = append(spans, traceSpan{
			TraceID:      entry.TraceID,
			SpanID:       entry.SpanID,
			ParentSpanID: entry.ParentSpanID,
			Service:      entry.ServiceName,
			Operation:    entry.Name,
			StartNs:      entry.TimeUnixNano,
			DurationNs:   entry.DurationNano,
			StatusCode:   statusCode,
			StatusMsg:    entry.StatusMessage,
			Attrs:        attrs,
		})
	}

	record := &traceRecord{TraceID: traceID, Spans: spans}
	return record, nil
}

// searchTraces searches for root spans with a given attribute key=value.
// Using isRoot=true means each result row is a distinct trace entry point, giving clean
// per-trace deduplication across all services without client-side grouping.
func searchTraces(queryURL, attribute, value, service string, limit int, sinceMinutes int) ([]traceRecord, error) {
	apiURL := fmt.Sprintf("%s/api/v3/query_range", queryURL)

	extraFilters := []signozFilter{
		{
			Key: signozFilterKey{
				Key:      attribute,
				Type:     "tag",
				DataType: "string",
				IsColumn: false,
			},
			Op:    "=",
			Value: value,
		},
		{
			Key: signozFilterKey{
				Key:      "isRoot",
				Type:     "tag",
				DataType: "bool",
				IsColumn: true,
			},
			Op:    "=",
			Value: true,
		},
	}

	req := buildQueryRangeRequest("traces", service, limit, sinceMinutes, extraFilters)

	var resp signozQueryRangeResponse
	if err := signozPost(apiURL, req, &resp, queryURL); err != nil {
		return nil, err
	}

	rows := extractListRows(resp)
	seen := make(map[string]bool)
	records := make([]traceRecord, 0, len(rows))
	for _, row := range rows {
		s := rowToTraceSpan(row)
		if s.TraceID == "" || seen[s.TraceID] {
			continue
		}
		seen[s.TraceID] = true
		records = append(records, traceRecord{TraceID: s.TraceID, Spans: []traceSpan{s}})
	}
	return records, nil
}

// fetchRootTraces fetches only root spans (entry points) for recent traces.
// It over-fetches by 10x then filters client-side to root spans, giving limit distinct executions.
func fetchRootTraces(queryURL, service string, limit int, sinceMinutes int) ([]traceRecord, error) {
	fetchLimit := limit * 10
	if fetchLimit > 500 {
		fetchLimit = 500
	}
	if fetchLimit < limit {
		fetchLimit = limit
	}

	apiURL := fmt.Sprintf("%s/api/v3/query_range", queryURL)
	req := buildQueryRangeRequest("traces", service, fetchLimit, sinceMinutes, nil)

	var resp signozQueryRangeResponse
	if err := signozPost(apiURL, req, &resp, queryURL); err != nil {
		return nil, err
	}

	rows := extractListRows(resp)

	// Keep only root spans (no parent), deduplicate by traceID, preserve DESC timestamp order.
	seen := make(map[string]bool)
	records := make([]traceRecord, 0, limit)
	for _, row := range rows {
		s := rowToTraceSpan(row)
		if s.ParentSpanID != "" {
			continue
		}
		if seen[s.TraceID] {
			continue
		}
		seen[s.TraceID] = true
		records = append(records, traceRecord{TraceID: s.TraceID, Spans: []traceSpan{s}})
		if len(records) >= limit {
			break
		}
	}
	return records, nil
}

// --- Log types and queries ---

type logEntry struct {
	Timestamp int64  // Unix nanoseconds
	Body      string
	Service   string
	Severity  string
	TraceID   string
	SpanID    string
}

// fetchLogsForTrace queries SigNoz for log records associated with a traceID.
// Returns empty slice (not error) if the backend has no logs for the trace.
func fetchLogsForTrace(queryURL, traceID string) ([]logEntry, error) {
	apiURL := fmt.Sprintf("%s/api/v3/query_range", queryURL)

	traceFilter := signozFilter{
		Key:   signozFilterKey{Key: "traceID", Type: "tag", DataType: "string", IsColumn: true},
		Op:    "=",
		Value: traceID,
	}
	// Use a 2-hour window — bounded by traceID filter so volume is small regardless.
	req := buildQueryRangeRequest("logs", "", 200, 120, []signozFilter{traceFilter})

	var resp signozQueryRangeResponse
	if err := signozPost(apiURL, req, &resp, queryURL); err != nil {
		// Log backend might not be populated; treat as empty rather than error.
		return nil, nil //nolint
	}

	rows := extractListRows(resp)
	entries := make([]logEntry, 0, len(rows))
	for _, row := range rows {
		d := row.Data
		getString := func(key string) string {
			if v, ok := d[key]; ok {
				if s, ok := v.(string); ok {
					return s
				}
			}
			return ""
		}
		getInt64 := func(key string) int64 {
			if v, ok := d[key]; ok {
				switch n := v.(type) {
				case float64:
					return int64(n)
				case int64:
					return n
				}
			}
			return 0
		}

		tsNs := getInt64("timestamp")
		if tsNs == 0 {
			if t, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil {
				tsNs = t.UnixNano()
			}
		}

		svc := getString("serviceName")
		if svc == "" {
			svc = getString("resources.service.name")
		}

		entries = append(entries, logEntry{
			Timestamp: tsNs,
			Body:      getString("body"),
			Service:   svc,
			Severity:  getString("severityText"),
			TraceID:   getString("traceID"),
			SpanID:    getString("spanID"),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp < entries[j].Timestamp
	})
	return entries, nil
}

// --- Helpers ---

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

// spanHasError returns true if a span has an error status code.
func spanHasError(s *traceSpan) bool {
	return strings.ToLower(s.StatusCode) == "error" || s.StatusCode == "2"
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

	// Build parent map: spanID -> parentSpanID.
	parentMap := make(map[string]string)
	for _, sp := range r.Spans {
		if sp.ParentSpanID != "" {
			parentMap[sp.SpanID] = sp.ParentSpanID
		}
	}

	// Sort spans by start time ascending.
	spans := make([]traceSpan, len(r.Spans))
	copy(spans, r.Spans)
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].StartNs < spans[j].StartNs
	})

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

		// Compute depth.
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

		for k, v := range sp.Attrs {
			if businessKeys[k] {
				fmt.Fprintf(&sb, "%s  %s: %s\n", indent, k, v)
			}
		}
		for k, v := range sp.Attrs {
			if httpKeys[k] {
				fmt.Fprintf(&sb, "%s  %s: %s\n", indent, k, v)
			}
		}
		if status == "ERROR" {
			for k, v := range sp.Attrs {
				if k == "error.message" || k == "exception.message" || k == "exception.type" {
					fmt.Fprintf(&sb, "%s  %s: %s\n", indent, k, v)
				}
			}
			if sp.StatusMsg != "" {
				fmt.Fprintf(&sb, "%s  status.message: %s\n", indent, sp.StatusMsg)
			}
		}
	}
	return sb.String()
}

// formatExecutionView formats a unified execution view: span tree + correlated logs.
func formatExecutionView(record *traceRecord, logs []logEntry) string {
	var sb strings.Builder

	root := rootSpan(record)

	// Header
	fmt.Fprintf(&sb, "Trace: %s\n", record.TraceID)
	if root != nil {
		ts := time.Unix(0, root.StartNs).Local().Format("2006-01-02 15:04:05.000")
		durationMs := float64(root.DurationNs) / 1e6
		status := "ok"
		if spanHasError(root) {
			status = "ERROR"
		}
		fmt.Fprintf(&sb, "Started:  %s\n", ts)
		fmt.Fprintf(&sb, "Duration: %.1fms\n", durationMs)
		fmt.Fprintf(&sb, "Status:   %s\n", status)

		// Unique services involved
		seen := make(map[string]bool)
		var services []string
		for _, sp := range record.Spans {
			if sp.Service != "" && !seen[sp.Service] {
				seen[sp.Service] = true
				services = append(services, sp.Service)
			}
		}
		if len(services) > 1 {
			sort.Strings(services)
			fmt.Fprintf(&sb, "Services: %s\n", strings.Join(services, ", "))
		}
	}

	// Span tree
	sb.WriteString("\nSPAN TREE:\n")
	sb.WriteString(formatSpanTree(record))

	// Correlated logs (from OTEL log exporter)
	if len(logs) > 0 {
		sb.WriteString("\nCORRELATED LOGS:\n")
		currentSvc := ""
		for _, log := range logs {
			if log.Service != currentSvc {
				fmt.Fprintf(&sb, "--- %s ---\n", log.Service)
				currentSvc = log.Service
			}
			ts := ""
			if log.Timestamp > 0 {
				ts = time.Unix(0, log.Timestamp).Local().Format("15:04:05.000") + " "
			}
			sev := log.Severity
			if sev == "" {
				sev = "INFO"
			}
			body := log.Body
			if len(body) > 300 {
				body = body[:297] + "..."
			}
			fmt.Fprintf(&sb, "  %s%s %s\n", ts, sev, body)
		}
	} else {
		sb.WriteString("\nCORRELATED LOGS: none (services may not export logs via OTEL)\n")
	}

	return sb.String()
}
