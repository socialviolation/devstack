package signoz

import "testing"

func TestStackFilter_Empty(t *testing.T) {
	if got := stackFilter(""); got != nil {
		t.Errorf("stackFilter(\"\") = %v, want nil", got)
	}
}

func TestStackFilter_ResourceAttribute(t *testing.T) {
	got := stackFilter("perf")
	if len(got) != 1 {
		t.Fatalf("stackFilter(\"perf\") returned %d filters, want 1", len(got))
	}
	f := got[0]
	if f.Key.Key != "devstack.stack" {
		t.Errorf("filter key = %q, want devstack.stack", f.Key.Key)
	}
	if f.Key.Type != "resource" {
		t.Errorf("filter type = %q, want resource", f.Key.Type)
	}
	if f.Op != "=" {
		t.Errorf("filter op = %q, want =", f.Op)
	}
	if f.Value != "perf" {
		t.Errorf("filter value = %v, want perf", f.Value)
	}
}

func TestBuildQueryRangeRequest_IncludesStackFilter(t *testing.T) {
	req := buildQueryRangeRequest("traces", "api", 10, 0, stackFilter("perf"))
	items := req.CompositeQuery.BuilderQueries["A"].Filters.Items

	var sawStack, sawService bool
	for _, it := range items {
		if it.Key.Key == "devstack.stack" && it.Key.Type == "resource" && it.Value == "perf" {
			sawStack = true
		}
		if it.Key.Key == "serviceName" && it.Value == "api" {
			sawService = true
		}
	}
	if !sawStack {
		t.Errorf("built query missing devstack.stack resource filter: %+v", items)
	}
	if !sawService {
		t.Errorf("built query missing serviceName filter: %+v", items)
	}
}
