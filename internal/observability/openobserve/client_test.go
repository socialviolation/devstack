package openobserve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/observability"
)

// captureServer records the SQL of each search and replies with fixed hits.
func captureServer(t *testing.T, hits []map[string]any) (*httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query struct {
				SQL string `json:"sql"`
			} `json:"query"`
		}
		json.Unmarshal(body, &req)
		queries = append(queries, r.URL.RawQuery+" :: "+req.Query.SQL)
		json.NewEncoder(w).Encode(map[string]any{"hits": hits})
	}))
	t.Cleanup(srv.Close)
	return srv, &queries
}

// Resource attributes are flattened with a service_ prefix in the traces stream
// but not in the logs stream, so the workspace filter cannot use one column.
func TestWorkspaceFilterColumnPerStream(t *testing.T) {
	if got := workspaceFilter("navexa", "traces"); len(got) != 1 || !strings.Contains(got[0], "service_devstack_workspace") {
		t.Errorf("traces filter = %v, want service_devstack_workspace", got)
	}
	if got := workspaceFilter("navexa", "logs"); len(got) != 1 || strings.Contains(got[0], "service_devstack_workspace") {
		t.Errorf("logs filter = %v, want unprefixed devstack_workspace", got)
	}
	if got := workspaceFilter("", "traces"); got != nil {
		t.Errorf("empty workspace should not filter, got %v", got)
	}
}

func TestQueryTracesScopesEveryQueryToWorkspace(t *testing.T) {
	srv, queries := captureServer(t, []map[string]any{{"trace_id": "abc"}})
	c := NewClient(srv.URL, "token")

	if _, err := c.QueryTraces(context.Background(), observability.TraceQuery{Workspace: "navexa"}); err != nil {
		t.Fatal(err)
	}

	if len(*queries) != 2 {
		t.Fatalf("expected a find query and a span fetch, got %d: %v", len(*queries), *queries)
	}
	for _, q := range *queries {
		if !strings.Contains(q, `service_devstack_workspace = 'navexa'`) {
			t.Errorf("query not scoped to the workspace: %s", q)
		}
	}
}

// A trace ID is globally unique but still belongs to one workspace; looking one
// up must not reach into another project's telemetry.
func TestQueryTracesByIDStaysScoped(t *testing.T) {
	srv, queries := captureServer(t, nil)
	c := NewClient(srv.URL, "token")

	if _, err := c.QueryTraces(context.Background(), observability.TraceQuery{
		TraceID: "abc", Workspace: "navexa",
	}); err != nil {
		t.Fatal(err)
	}
	if len(*queries) != 1 || !strings.Contains((*queries)[0], `service_devstack_workspace = 'navexa'`) {
		t.Errorf("trace-id lookup not scoped: %v", *queries)
	}
}

// Filtering on a null parent would fail outright until some span has a parent,
// because OpenObserve only materialises columns it has seen.
func TestQueryTracesAvoidsParentColumn(t *testing.T) {
	srv, queries := captureServer(t, nil)
	c := NewClient(srv.URL, "token")

	if _, err := c.QueryTraces(context.Background(), observability.TraceQuery{Workspace: "navexa"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains((*queries)[0], "reference_parent_span_id") {
		t.Errorf("root-span detection must not reference a possibly-absent column: %s", (*queries)[0])
	}
	if !strings.Contains((*queries)[0], "GROUP BY trace_id") {
		t.Errorf("expected traces to be grouped by trace_id: %s", (*queries)[0])
	}
}

func TestQueryLogsFiltersAndMaps(t *testing.T) {
	srv, queries := captureServer(t, []map[string]any{{
		"_timestamp":   float64(1785097706315790),
		"body":         "payment failed",
		"service_name": "checkout",
		"trace_id":     "abc",
		"span_id":      "def",
		"severity":     "ERROR",
	}})
	c := NewClient(srv.URL, "token")

	entries, err := c.QueryLogs(context.Background(), observability.LogQuery{
		Workspace: "navexa", TraceID: "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*queries)[0], "type=logs") {
		t.Errorf("logs must query the logs stream: %s", (*queries)[0])
	}
	if !strings.Contains((*queries)[0], `devstack_workspace = 'navexa'`) {
		t.Errorf("logs query not scoped: %s", (*queries)[0])
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].Body != "payment failed" || entries[0].Service != "checkout" || entries[0].Severity != "ERROR" {
		t.Errorf("entry = %+v", entries[0])
	}
	if got := entries[0].Timestamp.UTC(); got.IsZero() {
		t.Error("timestamp not mapped")
	}
}

func TestRowToSpanMapsSchema(t *testing.T) {
	span := rowToSpan(map[string]any{
		"span_id":                  "eee",
		"trace_id":                 "abc",
		"reference_parent_span_id": "parent",
		"service_name":             "checkout",
		"operation_name":           "POST /checkout",
		"duration":                 float64(120000), // microseconds
		"span_status":              "ERROR",
		"start_time":               float64(1785097680480396800),
		"http_status_code":         "500",
	})

	if span.DurationNano != 120_000_000 {
		t.Errorf("DurationNano = %d, want microseconds converted to nanos", span.DurationNano)
	}
	if span.ParentSpanID != "parent" || span.Service != "checkout" || span.Status != "ERROR" {
		t.Errorf("span = %+v", span)
	}
	if span.Attributes["http_status_code"] != "500" {
		t.Errorf("attributes = %v", span.Attributes)
	}
	if _, ok := span.Attributes["duration"]; ok {
		t.Error("structural columns should not leak into attributes")
	}
	if span.StartTime.IsZero() {
		t.Error("StartTime not mapped")
	}
}

func TestQuoteEscapesLiterals(t *testing.T) {
	if got := quote("o'brien"); got != "'o''brien'" {
		t.Errorf("quote() = %s", got)
	}
}

// A service can report itself under a different name than devstack knows it by,
// so a filter that matched only one of the two would silently return nothing.
func TestServiceFilterMatchesEitherIdentity(t *testing.T) {
	got := serviceFilter("navexa-api", "traces")
	for _, want := range []string{
		`service_devstack_service = 'navexa-api'`,
		`service_name = 'navexa-api'`,
		" OR ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("serviceFilter = %s, want it to contain %s", got, want)
		}
	}
}

// variantServer serves both the schema lookup and the grouping query.
func variantServer(t *testing.T, fields []string, hits []map[string]any) (*httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/streams") {
			schema := []map[string]string{}
			for _, f := range fields {
				schema = append(schema, map[string]string{"name": f})
			}
			json.NewEncoder(w).Encode(map[string]any{
				"list": []map[string]any{{"name": "default", "schema": schema}},
			})
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query struct {
				SQL string `json:"sql"`
			} `json:"query"`
		}
		json.Unmarshal(body, &req)
		queries = append(queries, req.Query.SQL)
		json.NewEncoder(w).Encode(map[string]any{"hits": hits})
	}))
	t.Cleanup(srv.Close)
	return srv, &queries
}

func TestListVariantsGroupsByIdentity(t *testing.T) {
	srv, queries := variantServer(t,
		[]string{"service_name", "service_devstack_service", "service_devstack_stack", "service_devstack_env"},
		[]map[string]any{
			{"service_name": "Navexa.API", "service_devstack_service": "navexa-api", "service_devstack_stack": "agent", "service_devstack_env": "dev"},
			{"service_name": "Navexa.API", "service_devstack_service": "navexa-api", "service_devstack_stack": "base", "service_devstack_env": "dev"},
		})
	c := NewClient(srv.URL, "token")

	got, err := c.ListVariants(context.Background(), observability.ServiceQuery{
		Workspace: "navexa", Since: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*queries)[0], "GROUP BY") || !strings.Contains((*queries)[0], `service_devstack_workspace = 'navexa'`) {
		t.Errorf("variant query = %s", (*queries)[0])
	}
	if len(got) != 2 {
		t.Fatalf("got %d variants, want one per stack: %+v", len(got), got)
	}
	if got[0].Stack != "agent" || got[1].Stack != "base" {
		t.Errorf("variants not sorted by stack: %+v", got)
	}
	if got[0].Service != "Navexa.API" || got[0].Devstack != "navexa-api" || got[0].Env != "dev" {
		t.Errorf("variant = %+v", got[0])
	}
}

// A column OpenObserve has not materialised yet must be left out of the query
// rather than failing it, which is what selecting an unknown field does.
func TestListVariantsSkipsAbsentColumns(t *testing.T) {
	srv, queries := variantServer(t, []string{"service_name"},
		[]map[string]any{{"service_name": "alpha"}})
	c := NewClient(srv.URL, "token")

	got, err := c.ListVariants(context.Background(), observability.ServiceQuery{Workspace: "navexa"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains((*queries)[0], "devstack_stack") || strings.Contains((*queries)[0], "devstack_env") {
		t.Errorf("query selected an absent column: %s", (*queries)[0])
	}
	if len(got) != 1 || got[0].Service != "alpha" || got[0].Stack != "" {
		t.Errorf("variants = %+v", got)
	}
}

func TestListVariantsEmptyStream(t *testing.T) {
	srv, _ := variantServer(t, nil, nil)
	c := NewClient(srv.URL, "token")

	got, err := c.ListVariants(context.Background(), observability.ServiceQuery{Workspace: "navexa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no variants from an empty stream, got %+v", got)
	}
}

// A stream exists only once something has written to it, so a workspace that
// emits traces but no logs must read as empty rather than failing the command.
func TestMissingStreamReadsAsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":20002,"message":"Search stream not found: default"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "token")

	logs, err := c.QueryLogs(context.Background(), observability.LogQuery{Workspace: "navexa"})
	if err != nil {
		t.Errorf("missing logs stream should not error: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected no logs, got %v", logs)
	}

	traces, err := c.QueryTraces(context.Background(), observability.TraceQuery{Workspace: "navexa"})
	if err != nil {
		t.Errorf("missing traces stream should not error: %v", err)
	}
	if len(traces) != 0 {
		t.Errorf("expected no traces, got %v", traces)
	}
}

// Real failures must still surface.
func TestSearchErrorsStillReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":20004,"message":"Search field not found: bogus"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "token")

	if _, err := c.QueryTraces(context.Background(), observability.TraceQuery{Workspace: "navexa"}); err == nil {
		t.Error("a genuine query error must not be swallowed")
	}
}

// Attribute values are quoted, but an attribute key lands in the query as a bare
// identifier. MCP hands that key straight to an agent, so a crafted one must be
// refused rather than executed.
func TestQueryTracesRejectsInjectedAttributeName(t *testing.T) {
	srv, queries := captureServer(t, nil)
	c := NewClient(srv.URL, "token")

	_, err := c.QueryTraces(context.Background(), observability.TraceQuery{
		Workspace: "navexa",
		Attribute: "x' OR '1'='1",
		Value:     "anything",
	})
	if err == nil {
		t.Fatal("a crafted attribute name was accepted")
	}
	if len(*queries) != 0 {
		t.Errorf("a query was sent despite the bad attribute: %v", *queries)
	}
}

func TestQueryTracesAcceptsRealAttributeNames(t *testing.T) {
	srv, _ := captureServer(t, nil)
	c := NewClient(srv.URL, "token")

	for _, attr := range []string{"portfolio.id", "http.status_code", "devstack.stack"} {
		if _, err := c.QueryTraces(context.Background(), observability.TraceQuery{
			Workspace: "navexa", Attribute: attr, Value: "1",
		}); err != nil {
			t.Errorf("attribute %q rejected: %v", attr, err)
		}
	}
}
