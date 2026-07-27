// Package telemetry answers whether a service's telemetry is actually arriving,
// per running variant. A service runs many times over — base, each feature
// stack — and "is it emitting?" has a different answer for each, so the evidence
// is reported per variant rather than per service name.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/observability"
)

// ServiceStatus is the evidence for one service's declared expectations, plus
// what each of its running variants actually reported.
type ServiceStatus struct {
	Service        string
	ExpectedTraces bool
	ExpectedLogs   bool
	Mode           string
	// Variants are the instances that reported telemetry in the window.
	Variants []VariantEvidence
	// TraceCount totals the spans seen across every variant.
	TraceCount     int
	LogEvidence    bool
	BackendReached bool
	Confidence     string
	Interpretation string
}

// VariantEvidence is what one instance of a service reported.
type VariantEvidence struct {
	Stack string
	Env   string
	// Reported is the name the service reports itself as, when it differs from
	// the name devstack knows it by.
	Reported string
	Spans    int
}

// DefaultWindow is how far back evidence is gathered when none is given.
const DefaultWindow = 15 * time.Minute

// Status reports, for every service declaring telemetry expectations, whether
// its telemetry is arriving and from which variants. backend may be nil (no
// queryable store, e.g. pure forwarding), in which case expectations are still
// listed but no evidence can be gathered.
func Status(workspacePath string, backend observability.Backend, window time.Duration) ([]ServiceStatus, error) {
	resolved, err := config.ResolveWorkspace(workspacePath)
	if err != nil {
		return nil, err
	}
	if window <= 0 {
		window = DefaultWindow
	}

	ctx := context.Background()
	var variants []observability.ServiceVariant
	loggers := map[string]bool{}
	backendReached := false
	if backend != nil {
		if v, err := backend.ListVariants(ctx, observability.ServiceQuery{Since: window}); err == nil {
			variants = v
			backendReached = true
		}
		// One sweep for log evidence, grouped by reporting service, rather than a
		// query per service.
		if entries, err := backend.QueryLogs(ctx, observability.LogQuery{Since: window, Limit: 500}); err == nil {
			for _, e := range entries {
				if e.Service != "" {
					loggers[e.Service] = true
				}
			}
		}
	}

	var statuses []ServiceStatus
	for _, name := range sortedServiceNames(resolved.Services) {
		service := resolved.Services[name]
		manifest := service.Manifest
		if manifest == nil {
			continue
		}

		evidence := evidenceFor(name, variants)
		total := 0
		for _, e := range evidence {
			total += e.Spans
		}

		logEvidence := loggers[name]
		for _, e := range evidence {
			if e.Reported != "" && loggers[e.Reported] {
				logEvidence = true
			}
		}

		mode := readMode(workspacePath, name)
		confidence, interpretation := classify(
			manifest.Telemetry.Traces.Expected, manifest.Telemetry.Logs.Expected,
			mode, backendReached, total, logEvidence, evidence)

		statuses = append(statuses, ServiceStatus{
			Service:        name,
			ExpectedTraces: manifest.Telemetry.Traces.Expected,
			ExpectedLogs:   manifest.Telemetry.Logs.Expected,
			Mode:           mode,
			Variants:       evidence,
			TraceCount:     total,
			LogEvidence:    logEvidence,
			BackendReached: backendReached,
			Confidence:     confidence,
			Interpretation: interpretation,
		})
	}
	return statuses, nil
}

// evidenceFor matches a devstack service to the variants reporting for it, by
// either identity: services routinely report a name of their own choosing that
// differs from the one devstack knows them by.
func evidenceFor(service string, variants []observability.ServiceVariant) []VariantEvidence {
	var out []VariantEvidence
	for _, v := range variants {
		if v.Devstack != service && v.Service != service {
			continue
		}
		e := VariantEvidence{Stack: v.Stack, Env: v.Env, Spans: v.Spans}
		if v.Service != "" && v.Service != service {
			e.Reported = v.Service
		}
		if e.Stack == "" {
			e.Stack = "base"
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stack < out[j].Stack })
	return out
}

// Summary renders one line naming each variant that reported, so a reader can
// tell which instance is emitting rather than only whether any of them is.
func (s ServiceStatus) Summary() string {
	if len(s.Variants) == 0 {
		return "no variant reported"
	}
	parts := make([]string, 0, len(s.Variants))
	for _, v := range s.Variants {
		part := fmt.Sprintf("%s=%d spans", v.Stack, v.Spans)
		if v.Env != "" {
			part += " env=" + v.Env
		}
		if v.Reported != "" {
			part += " as " + v.Reported
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func sortedServiceNames(services map[string]config.ResolvedService) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func classify(expectedTraces, expectedLogs bool, mode string, backendReached bool, traceCount int, logEvidence bool, variants []VariantEvidence) (string, string) {
	if !expectedTraces && !expectedLogs {
		if traceCount > 0 {
			return "high", "Telemetry is arriving, though the service declares no expectations."
		}
		return "low", "No telemetry expectations configured."
	}
	if mode == "collector-down" {
		return "inconclusive", "Telemetry is inconclusive because export is intentionally degraded."
	}
	if mode == "no-traces" || mode == "logs-only" {
		if logEvidence {
			return "partial", fmt.Sprintf("Scenario mode %s intentionally suppresses traces.", mode)
		}
		return "low", fmt.Sprintf("Scenario mode %s intentionally suppresses traces.", mode)
	}
	if !backendReached {
		return "inconclusive", "No queryable backend — telemetry could not be checked."
	}
	if traceCount > 0 {
		return "high", fmt.Sprintf("Observed telemetry from %d variant(s).", len(variants))
	}
	if logEvidence {
		return "partial", "Observed logs but no traces in the window."
	}
	return "low", "Expected telemetry was not observed in the window."
}

// readMode reports a service's scenario mode, a playground-style switch used to
// deliberately degrade telemetry so the classifier can be exercised.
func readMode(workspacePath, service string) string {
	data, err := os.ReadFile(filepath.Join(workspacePath, "state", service+".mode"))
	if err != nil {
		return "healthy"
	}
	mode := strings.TrimSpace(string(data))
	if mode == "" {
		return "healthy"
	}
	return mode
}
