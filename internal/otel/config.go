package otel

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkspaceContribution pairs a workspace name with the contribution its active
// plugin makes to the one host collector config.
type WorkspaceContribution struct {
	Workspace string
	Plugin    string
	// Local reports whether this workspace's telemetry stays on this machine.
	// Telemetry that arrives without a workspace attribute falls back to the
	// local destinations only: it cannot be attributed, so shipping it to
	// someone's upstream account would leak whatever emitted it.
	Local bool
	Contribution
}

// BuildConfig renders the host collector config: one OTLP receiver shared by
// every workspace, and each workspace's telemetry handled by its own plugin.
// When every workspace wants the same handling the pipelines are emitted
// directly; when they differ, a routing connector splits them on the
// devstack.workspace resource attribute every service is stamped with.
func BuildConfig(grpcPort, httpPort int, contribs []WorkspaceContribution) ([]byte, error) {
	if len(contribs) == 0 {
		return nil, fmt.Errorf("no workspace contributes to the collector configuration")
	}

	sorted := make([]WorkspaceContribution, len(contribs))
	copy(sorted, contribs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Workspace < sorted[j].Workspace })

	groups, err := groupByShape(sorted)
	if err != nil {
		return nil, err
	}

	cfg := map[string]any{
		"receivers": map[string]any{
			"otlp": map[string]any{
				"protocols": map[string]any{
					"grpc": map[string]any{"endpoint": fmt.Sprintf("0.0.0.0:%d", grpcPort)},
					"http": map[string]any{"endpoint": fmt.Sprintf("0.0.0.0:%d", httpPort)},
				},
			},
		},
	}

	if len(groups) == 1 {
		if err := renderSingle(cfg, groups[0]); err != nil {
			return nil, err
		}
	} else if err := renderRouted(cfg, groups); err != nil {
		return nil, err
	}

	return yaml.Marshal(cfg)
}

// shapeGroup is one distinct telemetry handling shared by one or more workspaces.
type shapeGroup struct {
	// key identifies the shape; workspaces with an identical key are merged.
	key        string
	workspaces []string
	local      bool
	Contribution
}

// groupByShape collapses workspaces whose contributions are identical, so the
// common case — every workspace on the same backend — needs no routing at all.
func groupByShape(contribs []WorkspaceContribution) ([]shapeGroup, error) {
	var groups []shapeGroup
	index := map[string]int{}
	for _, c := range contribs {
		raw, err := yaml.Marshal(c.Contribution)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", c.Workspace, err)
		}
		key := string(raw)
		if i, ok := index[key]; ok {
			groups[i].workspaces = append(groups[i].workspaces, c.Workspace)
			continue
		}
		index[key] = len(groups)
		groups = append(groups, shapeGroup{key: key, workspaces: []string{c.Workspace}, local: c.Local, Contribution: c.Contribution})
	}
	return groups, nil
}

func renderSingle(cfg map[string]any, g shapeGroup) error {
	putComponents(cfg, "processors", g.Processors)
	putComponents(cfg, "exporters", g.Exporters)
	putComponents(cfg, "connectors", g.Connectors)
	putComponents(cfg, "extensions", g.Extensions)

	pipelines := map[string]any{}
	for name, p := range map[string]Pipeline{"traces": g.Traces, "metrics": g.Metrics, "logs": g.Logs} {
		if len(p.Exporters) == 0 {
			continue
		}
		pipelines[name] = pipelineMap(Pipeline{
			Receivers:  []string{"otlp"},
			Processors: p.Processors,
			Exporters:  p.Exporters,
		})
	}
	for name, p := range g.Extra {
		pipelines[name] = pipelineMap(p)
	}
	if len(pipelines) == 0 {
		return fmt.Errorf("the plugin contributed no pipelines")
	}

	cfg["service"] = serviceMap(pipelines, extensionNames(g.Extensions), g.Telemetry)
	return nil
}

// renderRouted gives every shape its own suffixed copy of the components it
// declares, then fans the OTLP receiver out through a per-signal routing
// connector so one workspace's telemetry never reaches another's backend.
func renderRouted(cfg map[string]any, groups []shapeGroup) error {
	processors := map[string]any{}
	exporters := map[string]any{}
	connectors := map[string]any{}
	extensions := map[string]any{}
	pipelines := map[string]any{}
	telemetry := map[string]any{}

	// routes[signal] holds the routing table entries for that signal.
	routes := map[string][]any{}
	// defaults[signal] holds the pipelines unattributed telemetry falls back to,
	// preferring local destinations; allDefaults is the fallback when no shape is
	// local, so unattributed telemetry is still stored rather than dropped.
	defaults := map[string][]string{}
	allDefaults := map[string][]string{}

	for i, g := range groups {
		suffix := routeSuffix(g, i)
		rename := renamer(g, suffix)

		mergeRenamed(processors, g.Processors, rename)
		mergeRenamed(exporters, g.Exporters, rename)
		mergeRenamed(connectors, g.Connectors, rename)
		mergeRenamed(extensions, g.Extensions, rename)
		for k, v := range g.Telemetry {
			telemetry[k] = v
		}

		for signal, p := range map[string]Pipeline{"traces": g.Traces, "metrics": g.Metrics, "logs": g.Logs} {
			if len(p.Exporters) == 0 {
				continue
			}
			name := signal + "/" + suffix
			pipelines[name] = pipelineMap(Pipeline{
				Receivers:  []string{"routing/" + signal},
				Processors: renameAll(p.Processors, rename),
				Exporters:  renameAll(p.Exporters, rename),
			})
			if cond := workspaceCondition(g.workspaces); cond != "" {
				routes[signal] = append(routes[signal], map[string]any{
					"context":   "resource",
					"condition": cond,
					"pipelines": []string{name},
				})
			}
			allDefaults[signal] = append(allDefaults[signal], name)
			if g.local {
				defaults[signal] = append(defaults[signal], name)
			}
		}

		for name, p := range g.Extra {
			pipelines[qualify(name, suffix)] = pipelineMap(Pipeline{
				Receivers:  renameAll(p.Receivers, rename),
				Processors: renameAll(p.Processors, rename),
				Exporters:  renameAll(p.Exporters, rename),
			})
		}
	}

	// Telemetry that never got a workspace attribute cannot be attributed to a
	// project, so it goes to the local destinations rather than to someone's
	// upstream account. With no local destination it goes everywhere, since
	// dropping it silently is worse.
	for signal, entries := range routes {
		fallback := defaults[signal]
		if len(fallback) == 0 {
			fallback = allDefaults[signal]
		}
		connectors["routing/"+signal] = map[string]any{
			"error_mode":        "ignore",
			"default_pipelines": fallback,
			"table":             entries,
		}
		pipelines[signal+"/in"] = pipelineMap(Pipeline{
			Receivers: []string{"otlp"},
			Exporters: []string{"routing/" + signal},
		})
	}

	putComponents(cfg, "processors", processors)
	putComponents(cfg, "exporters", exporters)
	putComponents(cfg, "connectors", connectors)
	putComponents(cfg, "extensions", extensions)
	cfg["service"] = serviceMap(pipelines, extensionNames(extensions), telemetry)
	return nil
}

// workspaceCondition matches the resource attribute every devstack-managed
// service carries (see workspace.ManagedEnvFor).
func workspaceCondition(workspaces []string) string {
	var terms []string
	for _, w := range workspaces {
		terms = append(terms, fmt.Sprintf("attributes[\"devstack.workspace\"] == %q", w))
	}
	return strings.Join(terms, " or ")
}

// routeSuffix names a shape's pipelines and components after the workspace using
// it, falling back to an index when several workspaces share one shape.
func routeSuffix(g shapeGroup, i int) string {
	if len(g.workspaces) == 1 {
		return sanitise(g.workspaces[0])
	}
	return fmt.Sprintf("group%d", i)
}

func sanitise(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// renamer returns a component-ID rewriter that keeps every shape's components
// distinct, so two workspaces forwarding to different upstreams don't collide on
// the same exporter name.
func renamer(g shapeGroup, suffix string) func(string) string {
	owned := map[string]bool{}
	for _, m := range []map[string]any{g.Processors, g.Exporters, g.Connectors, g.Extensions} {
		for k := range m {
			owned[k] = true
		}
	}
	return func(id string) string {
		if !owned[id] {
			return id
		}
		return qualify(id, suffix)
	}
}

// qualify appends a suffix to a collector component ID, merging into an existing
// type/name split rather than producing a second slash.
func qualify(id, suffix string) string {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return id[:i] + "/" + id[i+1:] + "_" + suffix
	}
	return id + "/" + suffix
}

func renameAll(ids []string, rename func(string) string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, rename(id))
	}
	return out
}

func mergeRenamed(dst, src map[string]any, rename func(string) string) {
	for k, v := range src {
		dst[rename(k)] = v
	}
}

func putComponents(cfg map[string]any, key string, components map[string]any) {
	if len(components) == 0 {
		return
	}
	cfg[key] = components
}

func pipelineMap(p Pipeline) map[string]any {
	m := map[string]any{}
	if len(p.Receivers) > 0 {
		m["receivers"] = p.Receivers
	}
	if len(p.Processors) > 0 {
		m["processors"] = p.Processors
	}
	if len(p.Exporters) > 0 {
		m["exporters"] = p.Exporters
	}
	return m
}

func serviceMap(pipelines map[string]any, extensions []string, telemetry map[string]any) map[string]any {
	svc := map[string]any{"pipelines": pipelines}
	if len(extensions) > 0 {
		svc["extensions"] = extensions
	}
	if len(telemetry) > 0 {
		svc["telemetry"] = telemetry
	}
	return svc
}

func extensionNames(extensions map[string]any) []string {
	if len(extensions) == 0 {
		return nil
	}
	names := make([]string, 0, len(extensions))
	for k := range extensions {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
