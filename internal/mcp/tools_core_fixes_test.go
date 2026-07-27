package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

func listCoreTools(t *testing.T) map[string]listedTool {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	ws := &workspace.Workspace{Name: "navexa", Path: dir}
	cfg := &config.WorkspaceConfig{
		Deps:         map[string][]string{},
		Groups:       map[string][]string{},
		ServicePaths: map[string]string{},
	}

	s := server.NewMCPServer("test", "0.0.0")
	registerStatusTool(s, nil, cfg.ServicePaths, cfg, ws)
	registerTopologyTool(s, dir)
	registerStartTool(s, nil, "", cfg, ws)
	registerRestartTool(s, nil, "", cfg, ws)
	registerStopTool(s, nil, "", cfg, ws)
	registerConfigureTool(s, nil, ws)
	registerProcessLogsTool(s, nil, "", cfg, ws)
	registerInvestigateTool(s, nil, "", nil, "", dir, ws)
	registerTunnelTool(s, nil, ws)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			Tools []listedTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	tools := map[string]listedTool{}
	for _, tl := range out.Result.Tools {
		tools[tl.Name] = tl
	}
	return tools
}

func TestCoreToolAnnotationsAreHonest(t *testing.T) {
	tools := listCoreTools(t)

	want := map[string]struct{ readOnly, destructive, idempotent, openWorld bool }{
		"status":       {readOnly: true, destructive: false, idempotent: true, openWorld: false},
		"topology":     {readOnly: true, destructive: false, idempotent: true, openWorld: false},
		"process_logs": {readOnly: true, destructive: false, idempotent: true, openWorld: false},
		"investigate":  {readOnly: true, destructive: false, idempotent: true, openWorld: false},
		"start":        {readOnly: false, destructive: false, idempotent: true, openWorld: false},
		"restart":      {readOnly: false, destructive: false, idempotent: true, openWorld: false},
		"stop":         {readOnly: false, destructive: true, idempotent: true, openWorld: false},
		"configure":    {readOnly: false, destructive: true, idempotent: true, openWorld: false},
		"tunnel":       {readOnly: false, destructive: true, idempotent: false, openWorld: true},
	}

	for name, w := range want {
		tl, ok := tools[name]
		if !ok {
			t.Errorf("tool %q not registered", name)
			continue
		}
		a := tl.Annotations
		if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Errorf("tool %q has unset annotations: %+v", name, a)
			continue
		}
		if *a.ReadOnlyHint != w.readOnly {
			t.Errorf("tool %q readOnlyHint = %v, want %v", name, *a.ReadOnlyHint, w.readOnly)
		}
		if *a.DestructiveHint != w.destructive {
			t.Errorf("tool %q destructiveHint = %v, want %v", name, *a.DestructiveHint, w.destructive)
		}
		if *a.IdempotentHint != w.idempotent {
			t.Errorf("tool %q idempotentHint = %v, want %v", name, *a.IdempotentHint, w.idempotent)
		}
		if *a.OpenWorldHint != w.openWorld {
			t.Errorf("tool %q openWorldHint = %v, want %v", name, *a.OpenWorldHint, w.openWorld)
		}
	}
}

func TestReadOnlyToolsAreMarkedReadOnly(t *testing.T) {
	tools := listCoreTools(t)
	for _, name := range []string{"status", "process_logs", "investigate", "topology"} {
		tl, ok := tools[name]
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tl.Annotations.ReadOnlyHint == nil || !*tl.Annotations.ReadOnlyHint {
			t.Errorf("%q must be readOnlyHint=true, got %v", name, tl.Annotations.ReadOnlyHint)
		}
		if tl.Annotations.DestructiveHint == nil || *tl.Annotations.DestructiveHint {
			t.Errorf("%q must be destructiveHint=false, got %v", name, tl.Annotations.DestructiveHint)
		}
	}
}

func TestStartAndTopologyToolsRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}

	RegisterTools(s, nil, "", nil, ws.Name, ws.Path, ws)

	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	listing := string(data)
	for _, name := range []string{"start", "topology"} {
		if !strings.Contains(listing, `"`+name+`"`) {
			t.Errorf("tools/list missing %q; got %s", name, listing)
		}
	}
}

func TestStopDescriptionStatesStackScoping(t *testing.T) {
	desc := listCoreTools(t)["stop"].Description
	for _, want := range []string{
		"stops the default service for this repo",
		"Stopping everything requires all=true",
		"never base's",
		"touches only that stack's instances",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("stop description missing %q; got %s", want, desc)
		}
	}
}

func TestInvestigateDescribesPerModeFiltering(t *testing.T) {
	tools := listCoreTools(t)
	desc := tools["investigate"].Description
	if !strings.Contains(desc, "every other filter is ignored, including service, stack, since_minutes, limit and errors_only") {
		t.Errorf("investigate description does not state that mode 1 ignores the other filters; got %s", desc)
	}

	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	registerInvestigateTool(s, nil, "", nil, "", ws.Path, ws)
	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Result.Tools[0].InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	stackDesc := schema.Properties["stack"].Description
	if !strings.Contains(stackDesc, "mode 2") || !strings.Contains(stackDesc, "mode 3") || !strings.Contains(stackDesc, "not stack-filtered") {
		t.Errorf("stack param does not state where it is applied; got %s", stackDesc)
	}
}

func TestCoreLogEmptyNoteNamesFiltersAndWidening(t *testing.T) {
	note := coreLogEmptyNote(coreLogFilters{
		Service:      "api-service",
		Stack:        "perf",
		Lines:        100,
		Offset:       0,
		Grep:         "timeout",
		SinceRestart: true,
		ErrorsOnly:   true,
	})
	for _, want := range []string{
		"NOT that the service is healthy",
		"scope=service api-service",
		"stack=stack perf",
		"lines=100",
		"since_restart=true",
		"errors_only=true",
		`grep="timeout"`,
		"since_restart=false",
		"errors_only=false",
		"drop grep",
		"omit stack to read base",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q; got:\n%s", want, note)
		}
	}

	base := coreLogEmptyNote(coreLogFilters{Lines: 100})
	if !strings.Contains(base, "scope=every service of this instance") {
		t.Errorf("unscoped note should say it covered every service; got:\n%s", base)
	}
	if !strings.Contains(base, "stack=base") {
		t.Errorf("unscoped note should name the base stack scope; got:\n%s", base)
	}
	if strings.Contains(base, "drop grep") {
		t.Errorf("note should not suggest dropping a grep that was not set; got:\n%s", base)
	}
}

func TestCoreInvestigateEmptyNoteNamesFiltersAndWidening(t *testing.T) {
	note := coreInvestigateEmptyNote(coreInvestigateFilters{
		Service:       "api-service",
		ServiceSource: "this repo's default service, not asked for",
		Stack:         "base",
		SinceMinutes:  5,
		Limit:         3,
		ErrorsOnly:    false,
	})
	for _, want := range []string{
		"NOT that the service is healthy, idle or uninstrumented",
		"stack=base only",
		"service=api-service (this repo's default service, not asked for)",
		"window=last 5 minute(s)",
		"limit=3",
		"errors_only=false",
		"raise since_minutes above 5",
		"stack='all'",
		"observability tool's status action",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q; got:\n%s", want, note)
		}
	}

	withErrors := coreInvestigateEmptyNote(coreInvestigateFilters{
		Stack:                   "",
		SinceMinutes:            60,
		Limit:                   3,
		ErrorsOnly:              true,
		MatchedBeforeErrorsOnly: 7,
	})
	if !strings.Contains(withErrors, "7 execution(s) matched everything except errors_only") {
		t.Errorf("note should report what errors_only discarded; got:\n%s", withErrors)
	}
	if !strings.Contains(withErrors, "stack=all instances") {
		t.Errorf("note should say an empty stack filter means all instances; got:\n%s", withErrors)
	}
	if !strings.Contains(withErrors, "service=(none — every service)") {
		t.Errorf("note should say no service filter was applied; got:\n%s", withErrors)
	}
}

func TestCoreStartOrderPutsDependenciesFirst(t *testing.T) {
	cfg := &config.WorkspaceConfig{
		Deps: map[string][]string{
			"api":     {"db", "cache"},
			"cache":   {"db"},
			"web":     {"api"},
			"db":      nil,
			"lonely":  nil,
			"ignored": {"db"},
		},
	}

	ordered, err := coreStartOrder(cfg, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, s := range ordered {
		pos[s] = i
	}
	for _, svc := range []string{"db", "cache", "api", "web"} {
		if _, ok := pos[svc]; !ok {
			t.Fatalf("coreStartOrder(web) = %v, missing %q", ordered, svc)
		}
	}
	if pos["db"] > pos["cache"] || pos["cache"] > pos["api"] || pos["api"] > pos["web"] {
		t.Errorf("coreStartOrder(web) = %v, dependencies must come first", ordered)
	}

	union, err := coreStartOrder(cfg, []string{"api", "lonely"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, s := range union {
		seen[s]++
	}
	for s, n := range seen {
		if n != 1 {
			t.Errorf("coreStartOrder returned %q %d times: %v", s, n, union)
		}
	}

	cycle := &config.WorkspaceConfig{Deps: map[string][]string{"a": {"b"}, "b": {"a"}}}
	if _, err := coreStartOrder(cycle, []string{"a"}); err == nil {
		t.Error("coreStartOrder must reject a dependency cycle")
	}
}

func TestCoreRenderTopology(t *testing.T) {
	graph := &config.TopologyGraph{
		WorkspaceRoot: "/tmp/navexa",
		WorkspaceName: "navexa",
		Groups:        map[string][]string{"core": {"api", "db"}},
		Services: map[string]*config.ServiceTopology{
			"api": {Name: "api", Path: "/tmp/navexa/api", Groups: []string{"core"}, Dependencies: []string{"db"}, Calls: []string{"db"}},
			"db":  {Name: "db", Path: "/tmp/navexa/db", Groups: []string{"core"}, Dependents: []string{"api"}, CalledBy: []string{"api"}},
		},
		Issues: []config.TopologyIssue{{Severity: config.TopologyIssueWarning, Code: "x", Message: "something odd"}},
	}

	all, err := coreRenderTopology(graph, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"workspace: navexa", "Groups:", "core: api, db", "- api", "dependencies: db", "dependents: api", "called by: api", "something odd", "declared configuration, not runtime state"} {
		if !strings.Contains(all, want) {
			t.Errorf("topology output missing %q; got:\n%s", want, all)
		}
	}

	one, err := coreRenderTopology(graph, "db")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(one, "- api\n") {
		t.Errorf("single-service topology should not list other services; got:\n%s", one)
	}
	if !strings.Contains(one, "- db") {
		t.Errorf("single-service topology missing the service; got:\n%s", one)
	}

	if _, err := coreRenderTopology(graph, "nope"); err == nil {
		t.Error("unknown service must be an error")
	} else if !strings.Contains(err.Error(), "api, db") {
		t.Errorf("unknown-service error should list known services; got %v", err)
	}
}
