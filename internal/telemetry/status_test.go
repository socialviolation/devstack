package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/observability"
)

// fakeBackend stands in for a telemetry store, so evidence can be asserted
// without one running.
type fakeBackend struct {
	variants []observability.ServiceVariant
	logs     []observability.LogEntry
}

func (f *fakeBackend) QueryTraces(context.Context, observability.TraceQuery) ([][]observability.Span, error) {
	return nil, nil
}

func (f *fakeBackend) QueryLogs(context.Context, observability.LogQuery) ([]observability.LogEntry, error) {
	return f.logs, nil
}

func (f *fakeBackend) ListVariants(context.Context, observability.ServiceQuery) ([]observability.ServiceVariant, error) {
	return f.variants, nil
}

func writeTelemetryWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "devstack.workspace.yaml"), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./services/telemetry-good
      - ./services/telemetry-bad
      - ./services/logs-only
`)
	for _, name := range []string{"telemetry-good", "telemetry-bad", "logs-only"} {
		mustWrite(t, filepath.Join(dir, "services", name, "devstack.service.yaml"), `version: 1
service:
  name: `+name+`
runtime:
  run:
    command: python3 app.py
telemetry:
  traces:
    expected: true
  logs:
    expected: true
`)
	}
	mustWrite(t, filepath.Join(dir, "state", "telemetry-bad.mode"), "collector-down\n")
	mustWrite(t, filepath.Join(dir, "state", "logs-only.mode"), "logs-only\n")
	return dir
}

func statusesByService(t *testing.T, dir string, backend observability.Backend) map[string]ServiceStatus {
	t.Helper()
	statuses, err := Status(dir, backend, time.Minute)
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	out := map[string]ServiceStatus{}
	for _, s := range statuses {
		out[s.Service] = s
	}
	return out
}

func TestStatusClassifiesFromBackendEvidence(t *testing.T) {
	dir := writeTelemetryWorkspace(t)
	backend := &fakeBackend{
		variants: []observability.ServiceVariant{
			{Service: "telemetry-good", Devstack: "telemetry-good", Stack: "base", Spans: 12},
		},
		logs: []observability.LogEntry{{Service: "logs-only"}},
	}

	byService := statusesByService(t, dir, backend)
	if got := byService["telemetry-good"]; got.Confidence != "high" || got.TraceCount != 12 {
		t.Errorf("telemetry-good = %+v, want high confidence and 12 spans", got)
	}
	if got := byService["telemetry-bad"].Confidence; got != "inconclusive" {
		t.Errorf("telemetry-bad confidence = %q", got)
	}
	if got := byService["logs-only"].Confidence; got != "partial" {
		t.Errorf("logs-only confidence = %q, want partial from log evidence", got)
	}
}

// The whole point of per-variant evidence: base emitting says nothing about
// whether a stack's copy of the same service is.
func TestStatusReportsEvidencePerVariant(t *testing.T) {
	dir := writeTelemetryWorkspace(t)
	backend := &fakeBackend{
		variants: []observability.ServiceVariant{
			{Service: "telemetry-good", Devstack: "telemetry-good", Stack: "base", Spans: 5},
			{Service: "telemetry-good", Devstack: "telemetry-good", Stack: "perf", Env: "perf", Spans: 3},
		},
	}

	got := statusesByService(t, dir, backend)["telemetry-good"]
	if len(got.Variants) != 2 {
		t.Fatalf("variants = %+v, want one per stack", got.Variants)
	}
	if got.Variants[0].Stack != "base" || got.Variants[1].Stack != "perf" {
		t.Errorf("variants not sorted by stack: %+v", got.Variants)
	}
	if got.Variants[1].Env != "perf" {
		t.Errorf("stack variant lost its env: %+v", got.Variants[1])
	}
	if got.TraceCount != 8 {
		t.Errorf("TraceCount = %d, want the total across variants", got.TraceCount)
	}
	if summary := got.Summary(); summary != "base=5 spans, perf=3 spans env=perf" {
		t.Errorf("Summary() = %q", summary)
	}
}

// A service reporting itself under a different name must still be matched to
// the devstack service it belongs to, or its evidence reads as missing.
func TestStatusMatchesServiceReportingAnotherName(t *testing.T) {
	dir := writeTelemetryWorkspace(t)
	backend := &fakeBackend{
		variants: []observability.ServiceVariant{
			{Service: "Telemetry.Good", Devstack: "telemetry-good", Stack: "base", Spans: 4},
		},
	}

	got := statusesByService(t, dir, backend)["telemetry-good"]
	if got.TraceCount != 4 {
		t.Fatalf("evidence not matched by devstack identity: %+v", got)
	}
	if got.Variants[0].Reported != "Telemetry.Good" {
		t.Errorf("reported name not surfaced: %+v", got.Variants[0])
	}
}

// Without a queryable backend the check cannot run, and must say so rather than
// reporting an absence of telemetry it never looked for.
func TestStatusWithoutBackendIsInconclusive(t *testing.T) {
	dir := writeTelemetryWorkspace(t)

	got := statusesByService(t, dir, nil)["telemetry-good"]
	if got.Confidence != "inconclusive" {
		t.Errorf("confidence = %q, want inconclusive without a backend", got.Confidence)
	}
	if got.BackendReached {
		t.Error("BackendReached should be false when there is no backend")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
