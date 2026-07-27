package signoz

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/observability"
)

// signozServer fakes the query_range API, recording each request body so tests
// can assert on the filters that were actually sent.
func signozServer(t *testing.T, rows []map[string]any) (*httptest.Server, *[]queryRangeRequest) {
	t.Helper()
	var got []queryRangeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req queryRangeRequest
		json.Unmarshal(body, &req)
		got = append(got, req)

		list := make([]listRow, 0, len(rows))
		for _, d := range rows {
			list = append(list, listRow{Data: d})
		}
		json.NewEncoder(w).Encode(queryRangeResponse{
			Data: queryRangeData{Result: []queryResult{{QueryName: "A", List: list}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// filtersOf flattens every filter across the request's builder queries.
func filtersOf(req queryRangeRequest) []filter {
	var out []filter
	for _, q := range req.CompositeQuery.BuilderQueries {
		out = append(out, q.Filters.Items...)
	}
	return out
}

func hasFilter(req queryRangeRequest, key, value string) bool {
	for _, f := range filtersOf(req) {
		if f.Key.Key == key && f.Value == value {
			return true
		}
	}
	return false
}

// One backend can hold several workspaces, so every query has to carry the
// workspace filter or it answers with another project's traces.
func TestQueryTracesScopesToWorkspace(t *testing.T) {
	srv, reqs := signozServer(t, nil)
	c := NewClient(srv.URL, "")

	if _, err := c.QueryTraces(context.Background(), observability.TraceQuery{
		Workspace: "navexa", Service: "api",
	}); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) == 0 {
		t.Fatal("no query was sent")
	}
	if !hasFilter((*reqs)[0], "devstack.workspace", "navexa") {
		t.Errorf("query not scoped to the workspace: %+v", filtersOf((*reqs)[0]))
	}
}

func TestQueryTracesAppliesStackFilter(t *testing.T) {
	srv, reqs := signozServer(t, nil)
	c := NewClient(srv.URL, "")

	if _, err := c.QueryTraces(context.Background(), observability.TraceQuery{
		Workspace: "navexa", Stack: "perf",
	}); err != nil {
		t.Fatal(err)
	}
	if !hasFilter((*reqs)[0], "devstack.stack", "perf") {
		t.Errorf("stack filter missing: %+v", filtersOf((*reqs)[0]))
	}
}

func TestQueryTracesMapsRowsToSpans(t *testing.T) {
	srv, _ := signozServer(t, []map[string]any{{
		"traceID":       "abc",
		"spanID":        "span-1",
		"parentSpanID":  "",
		"serviceName":   "api",
		"name":          "GET /orders",
		"timestamp":     float64(1785097680480396800),
		"durationNano":  float64(120000000),
		"statusCode":    "STATUS_CODE_ERROR",
		"statusMessage": "boom",
	}})
	c := NewClient(srv.URL, "")

	traces, err := c.QueryTraces(context.Background(), observability.TraceQuery{Workspace: "navexa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 || len(traces[0]) != 1 {
		t.Fatalf("got %d traces", len(traces))
	}
	span := traces[0][0]
	if span.TraceID != "abc" || span.Service != "api" || span.Operation != "GET /orders" {
		t.Errorf("span = %+v", span)
	}
	if span.DurationNano != 120000000 {
		t.Errorf("DurationNano = %d", span.DurationNano)
	}
	if span.Status != "STATUS_CODE_ERROR" {
		t.Errorf("Status = %q", span.Status)
	}
	if span.StartTime.IsZero() {
		t.Error("StartTime not mapped")
	}
}

func TestQueryLogsScopesAndMaps(t *testing.T) {
	srv, reqs := signozServer(t, []map[string]any{{
		"body":        "payment failed",
		"serviceName": "api",
		"traceID":     "abc",
		"spanID":      "span-1",
		"severity":    "ERROR",
		"timestamp":   float64(1785097680480396800),
	}})
	c := NewClient(srv.URL, "")

	entries, err := c.QueryLogs(context.Background(), observability.LogQuery{
		Workspace: "navexa", TraceID: "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFilter((*reqs)[0], "devstack.workspace", "navexa") {
		t.Errorf("log query not scoped: %+v", filtersOf((*reqs)[0]))
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].Body != "payment failed" || entries[0].Service != "api" {
		t.Errorf("entry = %+v", entries[0])
	}
}

// Variants are what tell one running copy of a service from another, so they
// come back qualified by whichever devstack attributes the spans carry.
func TestListVariantsGroupsByStack(t *testing.T) {
	srv, _ := signozServer(t, []map[string]any{
		{"serviceName": "api", "devstack.service": "navexa-api", "devstack.stack": "base", "devstack.env": "dev"},
		{"serviceName": "api", "devstack.service": "navexa-api", "devstack.stack": "perf", "devstack.env": "perf"},
		{"serviceName": "api", "devstack.service": "navexa-api", "devstack.stack": "base", "devstack.env": "dev"},
	})
	c := NewClient(srv.URL, "")

	got, err := c.ListVariants(context.Background(), observability.ServiceQuery{
		Workspace: "navexa", Since: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d variants, want one per stack: %+v", len(got), got)
	}
	if got[0].Stack != "base" || got[1].Stack != "perf" {
		t.Errorf("variants not sorted by stack: %+v", got)
	}
	if got[0].Spans != 2 {
		t.Errorf("base variant counted %d spans, want 2", got[0].Spans)
	}
	if got[0].Devstack != "navexa-api" || got[1].Env != "perf" {
		t.Errorf("variant attributes lost: %+v", got)
	}
}

// A backend that is down must report the failure rather than an empty result
// that reads as "nothing is emitting".
func TestQueryTracesReportsUnreachableBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv.Close() // nothing is listening

	c := NewClient(srv.URL, "")
	if _, err := c.QueryTraces(context.Background(), observability.TraceQuery{Workspace: "navexa"}); err == nil {
		t.Error("expected an error from an unreachable backend")
	}
}
