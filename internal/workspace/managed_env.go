package workspace

import (
	"strings"

	"github.com/socialviolation/devstack/internal/config"
)

// ManagedEnv returns the devstack-computed env for each service, keyed by
// service name — the ladder's top rung. It attributes telemetry to the base
// workspace; see ManagedEnvFor for stack-scoped identity.
func ManagedEnv(ws *Workspace, serviceNames []string, envNames map[string]string) map[string]map[string]string {
	return ManagedEnvFor(ws, serviceNames, "", envNames)
}

// ManagedEnvFor is ManagedEnv with the caller's stack identity threaded in. OTEL
// export env is only pushed down when the workspace opts into observability;
// otherwise services are left un-instrumented by default. Each service also
// carries its workspace/stack/service/env identity as OTEL resource attributes.
// Every variant of a service — base, each stack, each config env — ships to the
// one shared backend, so these attributes are the only thing that tells them
// apart at query time. stack is the stack's short name, or "" for base;
// envNames maps a service to its active config env (see ActiveEnvNames).
func ManagedEnvFor(ws *Workspace, serviceNames []string, stack string, envNames map[string]string) map[string]map[string]string {
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
		attrs := []string{
			"devstack.workspace=" + ws.Name,
			"devstack.service=" + name,
			"devstack.stack=" + stackLabel,
		}
		if env := envNames[name]; env != "" {
			// Only the devstack-namespaced key. deployment.environment is a
			// conventional key that upstream dashboards key on, and it belongs to
			// whoever owns the destination — the forwarding plugin sets it per
			// workspace. Emitting it here would put devstack in a race with that
			// for the meaning of a dimension someone else's dashboards depend on.
			attrs = append(attrs, "devstack.env="+env)
		}
		managed[name] = map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": ep,
			"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
			"OTEL_SERVICE_NAME":           name,
			"OTEL_RESOURCE_ATTRIBUTES":    strings.Join(attrs, ","),
		}
	}
	return managed
}

// ActiveEnvNames returns each service's effective config-env name, resolved the
// same way the generator resolves it (stack beats service beats workspace), so
// the env a variant reports is the env it actually runs under. stackEnv is the
// stack's env selection, or "" outside a stack.
func ActiveEnvNames(rw *config.ResolvedWorkspace, stackEnv string) map[string]string {
	if rw == nil || rw.Manifest == nil {
		return nil
	}
	out := make(map[string]string, len(rw.Services))
	for name, svc := range rw.Services {
		svcEnv := ""
		if svc.Manifest != nil {
			svcEnv = svc.Manifest.Service.Env
		}
		if env := config.ActiveEnvName(rw.Manifest.Workspace.Env, svcEnv, stackEnv); env != "" {
			out[name] = env
		}
	}
	return out
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
