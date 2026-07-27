package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// safetyCallTool invokes a registered tool and returns the raw JSON-RPC response.
func safetyCallTool(t *testing.T, s *server.MCPServer, name string, args map[string]any) string {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s.HandleMessage(context.Background(), raw))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// safetyStopServer registers only the stop tool, with no daemon behind it: a
// call that reaches the daemon would fail loudly rather than silently pass.
func safetyStopServer(t *testing.T, defaultService string) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}
	cfg := &config.WorkspaceConfig{Groups: map[string][]string{"core": {"api"}}}
	registerStopTool(s, nil, defaultService, cfg, ws)
	return s
}

func TestStopWithNoArgumentsDoesNotStopEverything(t *testing.T) {
	out := safetyCallTool(t, safetyStopServer(t, ""), "stop", map[string]any{})
	if !strings.Contains(out, "no service specified") || !strings.Contains(out, "all=true") {
		t.Fatalf("a bare stop must refuse and point at all=true, got: %s", out)
	}
	if strings.Contains(out, "Stopped") {
		t.Fatalf("a bare stop must not stop anything, got: %s", out)
	}
}

func TestStopRejectsAllCombinedWithATarget(t *testing.T) {
	s := safetyStopServer(t, "")
	for _, args := range []map[string]any{
		{"all": true, "service": "api"},
		{"all": true, "group": "core"},
	} {
		out := safetyCallTool(t, s, "stop", args)
		if !strings.Contains(out, "all cannot be combined with service or group") {
			t.Fatalf("stop(%v) should be rejected, got: %s", args, out)
		}
	}
}

func TestStopSchemaExposesAllAndDefaultsToOneService(t *testing.T) {
	s := safetyStopServer(t, "api")
	resp := s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	listing := string(data)
	if !strings.Contains(listing, `"all"`) {
		t.Fatalf("stop must expose an all parameter: %s", listing)
	}
	if !strings.Contains(listing, "Stopping everything requires all=true") {
		t.Fatalf("stop must state that stopping everything is opt-in: %s", listing)
	}
	if strings.Contains(listing, "If neither is given, stops all services") {
		t.Fatalf("stop still advertises the old stop-everything default: %s", listing)
	}
}

// safetyResource builds one daemon resource with a runtime status and deploy time.
func safetyResource(name, runtime, deployTime string) tilt.UIResource {
	var r tilt.UIResource
	r.Metadata.Name = name
	r.Status.RuntimeStatus = runtime
	if deployTime != "" {
		r.Status.LastDeployTime = &deployTime
	}
	return r
}

func safetyView(rs ...tilt.UIResource) *tilt.TiltView {
	return &tilt.TiltView{UiResources: rs}
}

func TestWaitReturnsWhenResourcesRedeployAndSettle(t *testing.T) {
	views := []*tilt.TiltView{
		safetyView(safetyResource("navexa:api", "ok", "t0")),
		safetyView(safetyResource("navexa:api", "pending", "t0")),
		safetyView(safetyResource("navexa:api", "ok", "t1")),
	}
	calls := 0
	fetch := func() (*tilt.TiltView, error) {
		v := views[min(calls, len(views)-1)]
		calls++
		return v, nil
	}
	slept := time.Duration(0)
	baseline := map[string]string{"navexa:api": "t0"}
	states, settled, err := coreWaitForSettled(fetch, []string{"navexa:api"}, baseline, 60*time.Second, time.Second, func(d time.Duration) { slept += d })
	if err != nil {
		t.Fatal(err)
	}
	if !settled {
		t.Fatalf("expected settled, got %#v", states)
	}
	if calls != 3 || slept != 2*time.Second {
		t.Fatalf("expected 3 polls and 2s slept, got %d polls and %s", calls, slept)
	}
	if states[0].status != "running" {
		t.Fatalf("final state = %q, want running", states[0].status)
	}
}

// A trigger takes a moment to land: the resource is still running its old
// process on the first poll, and a wait that stopped there would report a
// restart that had not happened.
func TestWaitDoesNotMistakeTheOldProcessForARestart(t *testing.T) {
	calls := 0
	fetch := func() (*tilt.TiltView, error) {
		calls++
		return safetyView(safetyResource("navexa:api", "ok", "t0")), nil
	}
	baseline := map[string]string{"navexa:api": "t0"}
	states, settled, err := coreWaitForSettled(fetch, []string{"navexa:api"}, baseline, 4*time.Second, 2*time.Second, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if settled {
		t.Fatal("an un-redeployed resource must not report settled")
	}
	if calls != 3 {
		t.Fatalf("expected polls at 0,2,4s, got %d", calls)
	}
	report := coreWaitReport(states, settled, 4*time.Second)
	if !strings.Contains(report, "no new deploy yet") {
		t.Fatalf("report must say the restart was not observed: %q", report)
	}
}

func TestWaitIsBoundedAndReportsTheUnsettledState(t *testing.T) {
	calls := 0
	fetch := func() (*tilt.TiltView, error) {
		calls++
		var r tilt.UIResource
		r.Metadata.Name = "navexa:api"
		r.Status.UpdateStatus = "running"
		return safetyView(r), nil
	}
	states, settled, err := coreWaitForSettled(fetch, []string{"navexa:api"}, nil, 10*time.Second, 2*time.Second, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if settled {
		t.Fatal("a resource stuck building must not report settled")
	}
	if calls != 6 {
		t.Fatalf("expected the poll to stop at the 10s timeout (polls at 0,2,4,6,8,10s), got %d", calls)
	}
	if states[0].status != "building" {
		t.Fatalf("state = %q, want building", states[0].status)
	}
	report := coreWaitReport(states, settled, 10*time.Second)
	if !strings.Contains(report, "navexa:api=building") || !strings.Contains(report, "10s") {
		t.Fatalf("timeout report must name the state and the wait: %q", report)
	}
	if strings.Contains(report, "after waiting:") {
		t.Fatalf("timeout must not read as success: %q", report)
	}
}

// A failed build never advances the deploy time, so 'erroring' has to end the
// wait or every failure would burn the full timeout.
func TestWaitStopsOnATerminalState(t *testing.T) {
	fetch := func() (*tilt.TiltView, error) {
		return safetyView(safetyResource("navexa:api", "error", "t0")), nil
	}
	states, settled, err := coreWaitForSettled(fetch, []string{"navexa:api"}, map[string]string{"navexa:api": "t0"}, 60*time.Second, time.Second, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if !settled || states[0].status != "erroring" {
		t.Fatalf("erroring should end the wait, got settled=%v %#v", settled, states)
	}
}

func TestWaitReportsAnUnknownResourceRatherThanHanging(t *testing.T) {
	fetch := func() (*tilt.TiltView, error) { return safetyView(safetyResource("navexa:other", "ok", "t1")), nil }
	states, settled, err := coreWaitForSettled(fetch, []string{"navexa:api"}, nil, 10*time.Second, time.Second, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if !settled || states[0].status != "unknown" {
		t.Fatalf("missing resource should settle as unknown, got settled=%v %#v", settled, states)
	}
}

func TestWaitForSkipsPollingWhenNotAskedTo(t *testing.T) {
	if got := coreWaitFor(nil, []string{"navexa:api"}, nil, 0); got != "" {
		t.Fatalf("wait_seconds=0 must return immediately, got %q", got)
	}
}

func TestReloadModeClassification(t *testing.T) {
	cases := []struct {
		name  string
		cmd   string
		watch []string
		want  string
	}{
		{name: "dotnet watch", cmd: "dotnet watch run", want: coreReloadAuto},
		{name: "air", cmd: "air -c .air.toml", want: coreReloadAuto},
		{name: "uvicorn reload", cmd: "uvicorn app:app --reload", want: coreReloadAuto},
		{name: "vite", cmd: "vite", want: coreReloadAuto},
		{name: "next dev", cmd: "next dev", want: coreReloadAuto},
		{name: "plain go run", cmd: "go run .", want: coreReloadManual},
		{name: "plain dotnet run", cmd: "dotnet run", want: coreReloadManual},
		{name: "runtime watch rescues a static command", cmd: "go run .", watch: []string{"./internal"}, want: coreReloadAuto},
		{name: "no command and no watch", want: coreReloadUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &config.ServiceManifest{}
			m.Runtime.Run.Command = tc.cmd
			m.Runtime.Watch = tc.watch
			if got := coreReloadMode(m, ""); got != tc.want {
				t.Fatalf("coreReloadMode(%q, watch=%v) = %q, want %q", tc.cmd, tc.watch, got, tc.want)
			}
		})
	}
	if got := coreReloadMode(nil, ""); got != coreReloadUnknown {
		t.Fatalf("coreReloadMode(nil) = %q, want %q", got, coreReloadUnknown)
	}
}

func TestReloadModeResolvesPackageScripts(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"scripts":{"start":"ng serve --configuration=development","build":"ng build"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatal(err)
	}
	m := &config.ServiceManifest{}
	m.Runtime.Run.Command = "npm run start"
	if got := coreReloadMode(m, dir); got != coreReloadAuto {
		t.Fatalf("npm run start → ng serve should be %q, got %q", coreReloadAuto, got)
	}
	m.Runtime.Run.Command = "npm run build"
	if got := coreReloadMode(m, dir); got != coreReloadManual {
		t.Fatalf("npm run build should be %q, got %q", coreReloadManual, got)
	}
	m.Runtime.Run.Command = "npm run start"
	if got := coreReloadMode(m, t.TempDir()); got != coreReloadManual {
		t.Fatalf("an unresolvable script must stay %q, got %q", coreReloadManual, got)
	}
}

// "Stop everything" is a common ask this tool cannot satisfy alone: it is scoped
// to one instance, and the daemon only stops from the shell. Saying so is the
// difference between a true report and a false one.
func TestSafetyStopReportsWhatItLeavesRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Workspace{Name: "navexa", Path: t.TempDir()}

	if got := coreStillRunning(ws, false, ""); got != "" {
		t.Errorf("a targeted stop should add no note, got %q", got)
	}

	got := coreStillRunning(ws, true, "")
	if !strings.Contains(got, "host daemon itself is still up") {
		t.Errorf("stop-all must say the daemon survives: %q", got)
	}
	if !strings.Contains(got, "devstack workspace down") {
		t.Errorf("stop-all must name what does stop the daemon: %q", got)
	}
}
