package workspace

import "github.com/socialviolation/devstack/internal/config"

// ManagedEnv returns the devstack-computed env for each service, keyed by
// service name — the ladder's top rung. OTEL export env is only pushed down when
// the workspace opts into observability; otherwise services are left
// un-instrumented by default.
func ManagedEnv(ws *Workspace, serviceNames []string) map[string]map[string]string {
	managed := map[string]map[string]string{}
	if ws == nil || !config.ObservabilityEnabled(ws.Path) {
		return managed
	}
	ep := OtelOTLPEndpoint(ws)
	if ep == "" {
		return managed
	}
	for _, name := range serviceNames {
		managed[name] = map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": ep,
			"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
		}
	}
	return managed
}
