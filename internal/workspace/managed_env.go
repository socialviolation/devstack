package workspace

import (
	"fmt"

	"github.com/socialviolation/devstack/internal/config"
)

// ManagedEnv returns the devstack-computed env for each service, keyed by
// service name — the ladder's top rung. It attributes telemetry to the base
// workspace; see ManagedEnvFor for stack-scoped identity.
func ManagedEnv(ws *Workspace, serviceNames []string) map[string]map[string]string {
	return ManagedEnvFor(ws, serviceNames, "")
}

// ManagedEnvFor is ManagedEnv with the caller's stack identity threaded in. OTEL
// export env is only pushed down when the workspace opts into observability;
// otherwise services are left un-instrumented by default. Each service also
// carries its workspace/stack/service identity as OTEL resource attributes so its
// logs and traces are attributable to one stack's service. stack is the stack's
// short name, or "" for base.
func ManagedEnvFor(ws *Workspace, serviceNames []string, stack string) map[string]map[string]string {
	managed := map[string]map[string]string{}
	if ws == nil || !config.ObservabilityEnabled(ws.Path) {
		return managed
	}
	ep := OtelOTLPEndpoint(ws)
	if ep == "" {
		return managed
	}
	stackLabel := stack
	if stackLabel == "" {
		stackLabel = "base"
	}
	for _, name := range serviceNames {
		managed[name] = map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": ep,
			"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
			"OTEL_SERVICE_NAME":           name,
			"OTEL_RESOURCE_ATTRIBUTES": fmt.Sprintf(
				"devstack.workspace=%s,devstack.service=%s,devstack.stack=%s",
				ws.Name, name, stackLabel),
		}
	}
	return managed
}

// ManagedEnvKeys names the values devstack computes for every service, so a
// view can show that they exist without resolving a specific service.
func ManagedEnvKeys() []string {
	return []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_RESOURCE_ATTRIBUTES",
		"OTEL_SERVICE_NAME",
	}
}
