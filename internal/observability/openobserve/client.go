// Package openobserve queries a local OpenObserve instance through its SQL
// search API, mapping its flattened row shape onto the observability.Backend
// span and log types.
package openobserve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/socialviolation/devstack/internal/observability"
)

const (
	org    = "default"
	stream = "default"
)

// Client queries OpenObserve's /_search API.
type Client struct {
	baseURL string
	token   string // base64 basic-auth token
	http    *http.Client
}

// NewClient returns a client for an OpenObserve instance. token is the base64
// "email:password" basic-auth token.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type searchRequest struct {
	Query      searchQuery `json:"query"`
	SearchType string      `json:"search_type"`
}

type searchQuery struct {
	SQL       string `json:"sql"`
	StartTime int64  `json:"start_time"` // microseconds
	EndTime   int64  `json:"end_time"`   // microseconds
	From      int    `json:"from"`
	Size      int    `json:"size"`
}

type searchResponse struct {
	Hits  []map[string]any `json:"hits"`
	Total int              `json:"total"`
}

// search runs a SQL query against one stream type over a lookback window.
func (c *Client) search(ctx context.Context, streamType, sql string, since time.Duration, limit int) ([]map[string]any, error) {
	if since <= 0 {
		since = 5 * time.Minute
	}
	if limit <= 0 {
		limit = 100
	}
	now := time.Now()
	body := searchRequest{
		Query: searchQuery{
			SQL:       sql,
			StartTime: now.Add(-since).UnixMicro(),
			EndTime:   now.UnixMicro(),
			Size:      limit,
		},
		SearchType: "ui",
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/api/%s/_search?type=%s", c.baseURL, org, streamType)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openobserve unreachable at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var msg bytes.Buffer
		msg.ReadFrom(resp.Body)
		body := strings.TrimSpace(msg.String())
		// A stream is only created by its first record, so a workspace that emits
		// traces but no logs — or a machine with no telemetry at all yet — has
		// nothing to search. That is an empty result, not a failure.
		if isMissingStream(body) {
			return nil, nil
		}
		return nil, fmt.Errorf("openobserve search failed (%d): %s", resp.StatusCode, body)
	}

	var out searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode openobserve response: %w", err)
	}
	return out.Hits, nil
}

// isMissingStream reports whether an error body says the stream does not exist.
func isMissingStream(body string) bool {
	return strings.Contains(body, "stream not found") || strings.Contains(body, "\"code\":20002")
}

// QueryTraces returns whole traces: matching spans are found first, then every
// span of each matched trace is fetched so callers get complete trees.
func (c *Client) QueryTraces(ctx context.Context, req observability.TraceQuery) ([][]observability.Span, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 3
	}

	if req.TraceID != "" {
		spans, err := c.spansForTraces(ctx, []string{req.TraceID}, req.Workspace, req.Since)
		if err != nil || len(spans) == 0 {
			return nil, err
		}
		return spans, nil
	}

	where := workspaceFilter(req.Workspace, "traces")
	if req.SpanID != "" {
		where = append(where, fmt.Sprintf("span_id = %s", quote(req.SpanID)))
	}
	if req.Service != "" {
		where = append(where, serviceFilter(req.Service, "traces"))
	}
	if req.Stack != "" {
		where = append(where, fmt.Sprintf("%s = %s", attrColumn("devstack.stack", "traces"), quote(req.Stack)))
	}
	if req.Attribute != "" {
		where = append(where, fmt.Sprintf("%s = %s", attrColumn(req.Attribute, "traces"), quote(req.Value)))
	}

	// Group rather than filtering on a null parent: OpenObserve's schema is
	// dynamic, so reference_parent_span_id does not exist as a column until some
	// span has a parent, and querying it before then fails the whole search.
	sql := fmt.Sprintf(
		`SELECT trace_id, min(start_time) AS first_seen FROM %q%s GROUP BY trace_id ORDER BY first_seen DESC`,
		stream, whereClause(where))

	hits, err := c.search(ctx, "traces", sql, req.Since, limit)
	if err != nil {
		return nil, err
	}

	var traceIDs []string
	seen := map[string]bool{}
	for _, h := range hits {
		id := str(h["trace_id"])
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		traceIDs = append(traceIDs, id)
	}
	if len(traceIDs) == 0 {
		return nil, nil
	}
	return c.spansForTraces(ctx, traceIDs, req.Workspace, req.Since)
}

// spansForTraces fetches every span of the given traces, grouped per trace and
// ordered by start time.
func (c *Client) spansForTraces(ctx context.Context, traceIDs []string, workspace string, since time.Duration) ([][]observability.Span, error) {
	quoted := make([]string, 0, len(traceIDs))
	for _, id := range traceIDs {
		quoted = append(quoted, quote(id))
	}
	where := append(workspaceFilter(workspace, "traces"),
		fmt.Sprintf("trace_id IN (%s)", strings.Join(quoted, ", ")))
	sql := fmt.Sprintf(`SELECT * FROM %q%s ORDER BY start_time ASC`, stream, whereClause(where))

	hits, err := c.search(ctx, "traces", sql, since, 2000)
	if err != nil {
		return nil, err
	}

	grouped := map[string][]observability.Span{}
	for _, h := range hits {
		s := rowToSpan(h)
		grouped[s.TraceID] = append(grouped[s.TraceID], s)
	}

	out := make([][]observability.Span, 0, len(traceIDs))
	for _, id := range traceIDs {
		if spans, ok := grouped[id]; ok {
			out = append(out, spans)
		}
	}
	return out, nil
}

// QueryLogs returns log records, optionally correlated with a trace.
func (c *Client) QueryLogs(ctx context.Context, req observability.LogQuery) ([]observability.LogEntry, error) {
	where := workspaceFilter(req.Workspace, "logs")
	if req.TraceID != "" {
		where = append(where, fmt.Sprintf("trace_id = %s", quote(req.TraceID)))
	}
	if req.Service != "" {
		where = append(where, serviceFilter(req.Service, "logs"))
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	sql := fmt.Sprintf(`SELECT * FROM %q%s ORDER BY _timestamp DESC`, stream, whereClause(where))

	hits, err := c.search(ctx, "logs", sql, req.Since, limit)
	if err != nil {
		return nil, err
	}

	entries := make([]observability.LogEntry, 0, len(hits))
	for _, h := range hits {
		entries = append(entries, observability.LogEntry{
			Timestamp: microsToTime(num(h["_timestamp"])),
			Body:      firstString(h, "body", "message", "log"),
			Service:   str(h["service_name"]),
			TraceID:   str(h["trace_id"]),
			SpanID:    str(h["span_id"]),
			Severity:  firstString(h, "severity", "severity_text", "level"),
		})
	}
	return entries, nil
}

// ListVariants returns the distinct variants reporting telemetry. The grouping
// columns are discovered from the stream schema first: OpenObserve materialises
// a column only once some record carries it, and selecting one that does not yet
// exist fails the whole query.
func (c *Client) ListVariants(ctx context.Context, req observability.ServiceQuery) ([]observability.ServiceVariant, error) {
	fields, err := c.traceFields(ctx)
	if err != nil {
		return nil, err
	}

	type column struct {
		name   string
		assign func(*observability.ServiceVariant, string)
	}
	candidates := []column{
		{"service_name", func(v *observability.ServiceVariant, s string) { v.Service = s }},
		{attrColumn("devstack.service", "traces"), func(v *observability.ServiceVariant, s string) { v.Devstack = s }},
		{attrColumn("devstack.stack", "traces"), func(v *observability.ServiceVariant, s string) { v.Stack = s }},
		{attrColumn("devstack.env", "traces"), func(v *observability.ServiceVariant, s string) { v.Env = s }},
	}

	var cols []column
	var names []string
	for _, c := range candidates {
		if fields[c.name] {
			cols = append(cols, c)
			names = append(names, c.name)
		}
	}
	if len(cols) == 0 {
		return nil, nil
	}

	selected := strings.Join(names, ", ")
	sql := fmt.Sprintf(`SELECT %s, count(*) AS span_count FROM %q%s GROUP BY %s`,
		selected, stream, whereClause(workspaceFilter(req.Workspace, "traces")), selected)

	hits, err := c.search(ctx, "traces", sql, req.Since, 500)
	if err != nil {
		return nil, err
	}

	variants := make([]observability.ServiceVariant, 0, len(hits))
	for _, h := range hits {
		var v observability.ServiceVariant
		for _, c := range cols {
			c.assign(&v, str(h[c.name]))
		}
		v.Spans = int(num(h["span_count"]))
		if v.Service == "" && v.Devstack == "" {
			continue
		}
		variants = append(variants, v)
	}
	sort.Slice(variants, func(i, j int) bool {
		a, b := variants[i], variants[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Stack != b.Stack {
			return a.Stack < b.Stack
		}
		return a.Env < b.Env
	})
	return variants, nil
}

// traceFields returns the columns the traces stream has actually materialised.
func (c *Client) traceFields(ctx context.Context) (map[string]bool, error) {
	url := fmt.Sprintf("%s/api/%s/streams?type=traces&fetchSchema=true", c.baseURL, org)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Basic "+c.token)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openobserve unreachable at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openobserve stream schema failed (%d)", resp.StatusCode)
	}

	var out struct {
		List []struct {
			Name   string `json:"name"`
			Schema []struct {
				Name string `json:"name"`
			} `json:"schema"`
		} `json:"list"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode openobserve schema: %w", err)
	}

	fields := map[string]bool{}
	for _, st := range out.List {
		if st.Name != stream {
			continue
		}
		for _, f := range st.Schema {
			fields[f.Name] = true
		}
	}
	return fields, nil
}

// rowToSpan maps one OpenObserve trace row onto a Span. Attribute columns are
// carried through so callers can filter on them without a second query.
func rowToSpan(row map[string]any) observability.Span {
	s := observability.Span{
		SpanID:       str(row["span_id"]),
		TraceID:      str(row["trace_id"]),
		ParentSpanID: str(row["reference_parent_span_id"]),
		Service:      str(row["service_name"]),
		Operation:    str(row["operation_name"]),
		DurationNano: int64(num(row["duration"]) * 1000), // OpenObserve stores microseconds
		Status:       str(row["span_status"]),
		StartTime:    nanosToTime(num(row["start_time"])),
		Attributes:   map[string]string{},
	}
	for k, v := range row {
		switch k {
		case "span_id", "trace_id", "reference_parent_span_id", "service_name",
			"operation_name", "duration", "span_status", "start_time", "end_time",
			"_timestamp", "events", "links", "flags":
			continue
		}
		if sv := str(v); sv != "" {
			s.Attributes[k] = sv
		}
	}
	return s
}

// attrColumn maps an OTEL attribute key to the column OpenObserve flattens it
// into: dots become underscores, and resource attributes gain a service_ prefix
// in the traces stream but not in the logs stream.
func attrColumn(key, streamType string) string {
	col := strings.ReplaceAll(key, ".", "_")
	if streamType == "traces" && isResourceAttr(key) {
		return "service_" + col
	}
	return col
}

// isResourceAttr reports whether a key is one devstack stamps on the resource
// rather than on individual spans.
func isResourceAttr(key string) bool {
	return strings.HasPrefix(key, "devstack.") || key == "deployment.environment"
}

// serviceFilter matches a service by either identity: the name devstack knows it
// by (devstack.service, what the caller is standing in) or the name the service
// reports itself as (service_name, chosen in application code). They routinely
// differ — a repo devstack calls "navexa-api" may report itself as "Navexa.API" —
// and matching only one silently returns nothing.
func serviceFilter(service, streamType string) string {
	return fmt.Sprintf("(%s = %s OR service_name = %s)",
		attrColumn("devstack.service", streamType), quote(service), quote(service))
}

// workspaceFilter scopes a query to one workspace. The traces stream prefixes
// resource attributes with service_ while the logs stream does not, so the
// column name depends on which stream is being queried.
func workspaceFilter(workspace, streamType string) []string {
	if workspace == "" {
		return nil
	}
	col := attrColumn("devstack.workspace", streamType)
	return []string{fmt.Sprintf("%s = %s", col, quote(workspace))}
}

func whereClause(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// quote renders a SQL string literal, escaping embedded quotes.
func quote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}

func firstString(row map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(row[k]); s != "" {
			return s
		}
	}
	return ""
}

func microsToTime(us float64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(int64(us))
}

func nanosToTime(ns float64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(ns))
}
