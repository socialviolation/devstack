// Package signoz implements the observability.Backend interface for SigNoz.
package signoz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"devstack/internal/observability"
)

func init() {
	observability.RegisterBackend("signoz", func(url, apiKey string) observability.Backend {
		return NewClient(url, apiKey)
	})
}

// Client implements observability.Backend for a SigNoz instance.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient creates a new SigNoz client.
// apiKey is optional; if non-empty it is sent as "Authorization: Bearer <apiKey>" on all requests.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// --- HTTP helpers ---

func (c *Client) get(url string, dest interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("observability backend is not reachable at %s", c.baseURL)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("observability backend returned HTTP %d from %s", resp.StatusCode, url)
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("failed to decode observability response: %w", err)
	}
	return nil
}

func (c *Client) post(url string, body interface{}, dest interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("observability backend is not reachable at %s", c.baseURL)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("observability backend returned HTTP %d from %s", resp.StatusCode, url)
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("failed to decode observability response: %w", err)
	}
	return nil
}

// --- SigNoz API types (POST /api/v3/query_range) ---

type queryRangeRequest struct {
	Start          int64                  `json:"start"`
	End            int64                  `json:"end"`
	Step           int                    `json:"step"`
	Variables      map[string]interface{} `json:"variables"`
	CompositeQuery compositeQuery         `json:"compositeQuery"`
}

type compositeQuery struct {
	QueryType      string                   `json:"queryType"`
	PanelType      string                   `json:"panelType"`
	BuilderQueries map[string]builderQuery  `json:"builderQueries"`
}

type builderQuery struct {
	DataSource         string                 `json:"dataSource"`
	QueryName          string                 `json:"queryName"`
	Expression         string                 `json:"expression"`
	AggregateOperator  string                 `json:"aggregateOperator"`
	AggregateAttribute map[string]interface{} `json:"aggregateAttribute"`
	Filters            filters                `json:"filters"`
	OrderBy            []orderBy              `json:"orderBy"`
	Limit              int                    `json:"limit"`
	PageSize           int                    `json:"pageSize"`
	SelectColumns      []filterKey            `json:"selectColumns"`
}

type filters struct {
	Op    string   `json:"op"`
	Items []filter `json:"items"`
}

type filter struct {
	Key   filterKey   `json:"key"`
	Op    string      `json:"op"`
	Value interface{} `json:"value"`
}

type filterKey struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	DataType string `json:"dataType"`
	IsColumn bool   `json:"isColumn"`
}

type orderBy struct {
	ColumnName string `json:"columnName"`
	Order      string `json:"order"`
}

type queryRangeResponse struct {
	Status string           `json:"status"`
	Data   queryRangeData   `json:"data"`
}

type queryRangeData struct {
	ResultType string        `json:"resultType"`
	Result     []queryResult `json:"result"`
}

type queryResult struct {
	QueryName string    `json:"queryName"`
	Series    interface{} `json:"series"`
	List      []listRow `json:"list"`
}

type listRow struct {
	Timestamp string                 `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}

// --- SigNoz trace detail API types (GET /api/v1/traces/{id}) ---

type traceV1Item struct {
	StartTimestampMillis int64           `json:"startTimestampMillis"`
	EndTimestampMillis   int64           `json:"endTimestampMillis"`
	Columns              []string        `json:"columns"`
	Events               [][]interface{} `json:"events"`
	IsSubTree            bool            `json:"isSubTree"`
}

// --- Internal span type ---

type internalSpan struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Service      string
	Operation    string
	StartNs      int64
	DurationNs   int64
	StatusCode   string
	StatusMsg    string
	Attrs        map[string]string
}

func (s *internalSpan) toObservability() observability.Span {
	return observability.Span{
		SpanID:       s.SpanID,
		TraceID:      s.TraceID,
		ParentSpanID: s.ParentSpanID,
		Service:      s.Service,
		Operation:    s.Operation,
		DurationNano: s.DurationNs,
		Status:       s.StatusCode,
		Attributes:   s.Attrs,
		StartTime:    time.Unix(0, s.StartNs),
	}
}

// --- Query helpers ---

func selectColumnsFor(dataSource string) []filterKey {
	col := func(key, dataType string) filterKey {
		return filterKey{Key: key, Type: "tag", DataType: dataType, IsColumn: true}
	}
	switch dataSource {
	case "logs":
		return []filterKey{
			col("timestamp", "string"),
			col("body", "string"),
			col("severityText", "string"),
			col("traceID", "string"),
			col("spanID", "string"),
		}
	default: // "traces"
		return []filterKey{
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

func buildQueryRangeRequest(dataSource, service string, limit int, since time.Duration, extraFilters []filter) queryRangeRequest {
	now := time.Now()
	endMs := now.UnixMilli()
	startMs := now.Add(-since).UnixMilli()

	f := filters{
		Op:    "AND",
		Items: extraFilters,
	}
	if f.Items == nil {
		f.Items = []filter{}
	}

	if service != "" {
		f.Items = append(f.Items, filter{
			Key: filterKey{
				Key:      "serviceName",
				Type:     "tag",
				DataType: "string",
				IsColumn: true,
			},
			Op:    "=",
			Value: service,
		})
	}

	return queryRangeRequest{
		Start:     startMs,
		End:       endMs,
		Step:      60,
		Variables: map[string]interface{}{},
		CompositeQuery: compositeQuery{
			QueryType: "builder",
			PanelType: "list",
			BuilderQueries: map[string]builderQuery{
				"A": {
					DataSource:         dataSource,
					QueryName:          "A",
					Expression:         "A",
					AggregateOperator:  "noop",
					AggregateAttribute: map[string]interface{}{},
					Filters:            f,
					OrderBy: []orderBy{
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

func extractListRows(resp queryRangeResponse) []listRow {
	for _, result := range resp.Data.Result {
		if result.QueryName == "A" {
			return result.List
		}
	}
	return nil
}

func rowToInternalSpan(row listRow) internalSpan {
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

	startNs := getInt64("timestamp")
	if startNs == 0 {
		if t, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil {
			startNs = t.UnixNano()
		}
	}

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

	return internalSpan{
		TraceID:      getString("traceID"),
		SpanID:       getString("spanID"),
		ParentSpanID: getString("parentSpanID"),
		Service:      getString("serviceName"),
		Operation:    getString("name"),
		StartNs:      startNs,
		DurationNs:   getInt64("durationNano"),
		StatusCode:   statusCode,
		StatusMsg:    getString("statusMessage"),
		Attrs:        attrs,
	}
}

// --- Backend interface implementation ---

// QueryTraces implements observability.Backend.
func (c *Client) QueryTraces(ctx context.Context, req observability.TraceQuery) ([][]observability.Span, error) {
	if req.TraceID != "" {
		spans, err := c.fetchTraceByID(req.TraceID)
		if err != nil {
			return nil, err
		}
		if len(spans) == 0 {
			return nil, nil
		}
		return [][]observability.Span{spans}, nil
	}

	if req.SpanID != "" {
		traceID, err := c.traceIDForSpan(req.SpanID)
		if err != nil {
			return nil, err
		}
		if traceID == "" {
			return nil, nil
		}
		spans, err := c.fetchTraceByID(traceID)
		if err != nil {
			return nil, err
		}
		return [][]observability.Span{spans}, nil
	}

	since := req.Since
	if since == 0 {
		since = 5 * time.Minute
	}
	limit := req.Limit
	if limit == 0 {
		limit = 3
	}

	if req.Attribute != "" && req.Value != "" {
		return c.searchByAttribute(req.Attribute, req.Value, req.Service, limit, since)
	}

	return c.fetchRootTraces(req.Service, limit, since)
}

func (c *Client) fetchTraceByID(traceID string) ([]observability.Span, error) {
	apiURL := fmt.Sprintf("%s/api/v1/traces/%s", c.baseURL, traceID)

	var items []traceV1Item
	if err := c.get(apiURL, &items); err != nil {
		return nil, err
	}

	var spans []observability.Span
	for _, item := range items {
		colIdx := make(map[string]int, len(item.Columns))
		for i, col := range item.Columns {
			colIdx[col] = i
		}

		get := func(row []interface{}, col string) interface{} {
			if i, ok := colIdx[col]; ok && i < len(row) {
				return row[i]
			}
			return nil
		}
		getString := func(row []interface{}, col string) string {
			if v := get(row, col); v != nil {
				if s, ok := v.(string); ok {
					return s
				}
			}
			return ""
		}
		getStrings := func(row []interface{}, col string) []string {
			if v := get(row, col); v != nil {
				if arr, ok := v.([]interface{}); ok {
					out := make([]string, 0, len(arr))
					for _, x := range arr {
						if s, ok := x.(string); ok {
							out = append(out, s)
						}
					}
					return out
				}
			}
			return nil
		}
		getInt64Ms := func(row []interface{}, col string) int64 {
			if v := get(row, col); v != nil {
				switch n := v.(type) {
				case float64:
					return int64(n)
				case int64:
					return n
				}
			}
			return 0
		}

		for _, row := range item.Events {
			startMs := getInt64Ms(row, "__time")
			startNs := startMs * 1_000_000

			var durationNs int64
			if ds := getString(row, "DurationNano"); ds != "" {
				fmt.Sscanf(ds, "%d", &durationNs)
			}

			keys := getStrings(row, "TagsKeys")
			vals := getStrings(row, "TagsValues")
			attrs := make(map[string]string, len(keys))
			for i, k := range keys {
				if i < len(vals) {
					attrs[k] = vals[i]
				}
			}

			var parentSpanID string
			for _, ref := range getStrings(row, "References") {
				if idx := strings.Index(ref, "SpanId="); idx >= 0 {
					rest := ref[idx+7:]
					end := strings.IndexAny(rest, ", }")
					if end < 0 {
						end = len(rest)
					}
					candidate := strings.TrimSpace(rest[:end])
					if candidate != "" {
						parentSpanID = candidate
						break
					}
				}
			}

			statusCode := strings.ToLower(getString(row, "StatusCodeString"))
			if statusCode == "" || statusCode == "unset" {
				statusCode = "unset"
			}

			spans = append(spans, observability.Span{
				TraceID:      getString(row, "TraceId"),
				SpanID:       getString(row, "SpanId"),
				ParentSpanID: parentSpanID,
				Service:      getString(row, "ServiceName"),
				Operation:    getString(row, "Name"),
				DurationNano: durationNs,
				Status:       statusCode,
				Attributes:   attrs,
				StartTime:    time.Unix(0, startNs),
			})
		}
	}
	return spans, nil
}

func (c *Client) searchByAttribute(attribute, value, service string, limit int, since time.Duration) ([][]observability.Span, error) {
	apiURL := fmt.Sprintf("%s/api/v3/query_range", c.baseURL)

	extraFilters := []filter{
		{
			Key: filterKey{Key: attribute, Type: "tag", DataType: "string", IsColumn: false},
			Op:  "=", Value: value,
		},
		{
			Key: filterKey{Key: "parentSpanID", Type: "tag", DataType: "string", IsColumn: true},
			Op:  "=", Value: "",
		},
	}

	fetchLimit := limit * 5
	if fetchLimit < 10 {
		fetchLimit = 10
	}
	req := buildQueryRangeRequest("traces", service, fetchLimit, since, extraFilters)

	var resp queryRangeResponse
	if err := c.post(apiURL, req, &resp); err != nil {
		return nil, err
	}

	rows := extractListRows(resp)
	seen := make(map[string]bool)
	var result [][]observability.Span
	for _, row := range rows {
		sp := rowToInternalSpan(row)
		if sp.TraceID == "" || seen[sp.TraceID] {
			continue
		}
		seen[sp.TraceID] = true
		result = append(result, []observability.Span{sp.toObservability()})
	}
	return result, nil
}

func (c *Client) fetchRootTraces(service string, limit int, since time.Duration) ([][]observability.Span, error) {
	fetchLimit := limit * 10
	if fetchLimit > 500 {
		fetchLimit = 500
	}
	if fetchLimit < limit {
		fetchLimit = limit
	}

	apiURL := fmt.Sprintf("%s/api/v3/query_range", c.baseURL)
	req := buildQueryRangeRequest("traces", service, fetchLimit, since, nil)

	var resp queryRangeResponse
	if err := c.post(apiURL, req, &resp); err != nil {
		return nil, err
	}

	rows := extractListRows(resp)
	seen := make(map[string]bool)
	var result [][]observability.Span
	for _, row := range rows {
		sp := rowToInternalSpan(row)
		if sp.ParentSpanID != "" {
			continue
		}
		if seen[sp.TraceID] {
			continue
		}
		seen[sp.TraceID] = true
		result = append(result, []observability.Span{sp.toObservability()})
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

// QueryLogs implements observability.Backend.
func (c *Client) QueryLogs(ctx context.Context, req observability.LogQuery) ([]observability.LogEntry, error) {
	apiURL := fmt.Sprintf("%s/api/v3/query_range", c.baseURL)

	var extraFilters []filter
	if req.TraceID != "" {
		extraFilters = append(extraFilters, filter{
			Key:   filterKey{Key: "traceID", Type: "tag", DataType: "string", IsColumn: true},
			Op:    "=",
			Value: req.TraceID,
		})
	}

	since := req.Since
	if since == 0 {
		since = 2 * time.Hour
	}
	limit := req.Limit
	if limit == 0 {
		limit = 30
	}

	queryReq := buildQueryRangeRequest("logs", req.Service, limit, since, extraFilters)

	var resp queryRangeResponse
	if err := c.post(apiURL, queryReq, &resp); err != nil {
		return nil, nil // treat as empty, not error
	}

	rows := extractListRows(resp)
	entries := make([]observability.LogEntry, 0, len(rows))
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
		var ts time.Time
		if tsNs == 0 {
			if t, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil {
				ts = t
			}
		} else {
			ts = time.Unix(0, tsNs)
		}

		svc := getString("serviceName")
		if svc == "" {
			svc = getString("resources.service.name")
		}

		entries = append(entries, observability.LogEntry{
			Timestamp: ts,
			Body:      getString("body"),
			Service:   svc,
			Severity:  getString("severityText"),
			TraceID:   getString("traceID"),
			SpanID:    getString("spanID"),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
	return entries, nil
}

// ListServices implements observability.Backend.
// Queries for unique serviceName values from traces in the given time window.
func (c *Client) ListServices(ctx context.Context, since time.Duration) ([]string, error) {
	// Fetch a broad sample of spans and collect unique service names
	traces, err := c.fetchAllSpans(since, 500)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var services []string
	for _, sp := range traces {
		if sp.Service != "" && !seen[sp.Service] {
			seen[sp.Service] = true
			services = append(services, sp.Service)
		}
	}
	sort.Strings(services)
	return services, nil
}

// traceIDForSpan searches for a span by its SpanId and returns the containing traceID.
func (c *Client) traceIDForSpan(spanID string) (string, error) {
	apiURL := fmt.Sprintf("%s/api/v3/query_range", c.baseURL)
	extraFilters := []filter{
		{
			Key:   filterKey{Key: "spanId", Type: "tag", DataType: "string", IsColumn: true},
			Op:    "=",
			Value: spanID,
		},
	}
	req := buildQueryRangeRequest("traces", "", 1, 24*time.Hour, extraFilters)

	var resp queryRangeResponse
	if err := c.post(apiURL, req, &resp); err != nil {
		return "", err
	}
	rows := extractListRows(resp)
	if len(rows) == 0 {
		return "", nil
	}
	sp := rowToInternalSpan(rows[0])
	return sp.TraceID, nil
}

// fetchAllSpans fetches a flat list of spans (not grouped by trace) for service enumeration.
func (c *Client) fetchAllSpans(since time.Duration, limit int) ([]internalSpan, error) {
	apiURL := fmt.Sprintf("%s/api/v3/query_range", c.baseURL)
	req := buildQueryRangeRequest("traces", "", limit, since, nil)

	var resp queryRangeResponse
	if err := c.post(apiURL, req, &resp); err != nil {
		return nil, err
	}

	rows := extractListRows(resp)
	spans := make([]internalSpan, 0, len(rows))
	for _, row := range rows {
		spans = append(spans, rowToInternalSpan(row))
	}
	return spans, nil
}
