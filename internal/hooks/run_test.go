package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func stackUpEvent(services ...string) Event {
	return Event{
		Name:          config.EventStackUp,
		WorkspaceName: "navexa",
		Stack:         "feat",
		StackRoot:     "/stacks/feat",
		Branch:        "nick/feat",
		EnvName:       "dev",
		Services:      services,
		Book:          config.PortBook{"frontend": {"http": 20006}, "api": {"http": 20005}},
	}
}

func sourceWriting(t *testing.T, dir string, hooks ...config.Hook) Source {
	t.Helper()
	return Source{
		WorkspaceRoot:  dir,
		WorkspaceHooks: hooks,
		Services: map[string]ServiceRef{
			"frontend": {Path: dir},
			"api":      {Path: dir},
		},
	}
}

func TestRunExecutesHookAndStreamsPrefixedOutput(t *testing.T) {
	dir := t.TempDir()
	src := sourceWriting(t, dir, config.Hook{
		Name: "greet",
		On:   []string{config.EventStackUp},
		Run:  "echo hello; echo trailing >&2",
	})

	var out bytes.Buffer
	results, err := Run(stackUpEvent("frontend"), src, &out)
	if err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("results = %#v", results)
	}
	if !strings.Contains(out.String(), "hook greet │ hello") {
		t.Errorf("stdout not prefixed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "hook greet │ trailing") {
		t.Errorf("stderr not prefixed:\n%s", out.String())
	}
}

// The ${...} references a hook writes must resolve against the ports the stack
// was actually allocated — that is the whole point of a hook on an ephemeral
// stack, whose ports are not known when the hook is written.
func TestRunResolvesReferencesAgainstAllocatedPorts(t *testing.T) {
	dir := t.TempDir()
	src := sourceWriting(t, dir, config.Hook{
		Name:     "callback",
		On:       []string{config.EventStackUp},
		Services: []string{"frontend"},
		Run:      "printf '%s' \"$CALLBACK\" > callback.txt",
		Env:      map[string]string{"CALLBACK": "${self.url}/callback"},
	})

	var out bytes.Buffer
	if _, err := Run(stackUpEvent("frontend"), src, &out); err != nil {
		t.Fatalf("Run(): %v\n%s", err, out.String())
	}

	got := mustRead(t, filepath.Join(dir, "callback.txt"))
	if got != "http://localhost:20006/callback" {
		t.Fatalf("CALLBACK = %q, want the allocated port", got)
	}
}

func TestRunSetsContextEnvironment(t *testing.T) {
	dir := t.TempDir()
	src := sourceWriting(t, dir, config.Hook{
		Name:     "context",
		On:       []string{config.EventStackUp},
		Services: []string{"frontend"},
		Run:      "env | grep '^DEVSTACK_' | sort > env.txt",
	})

	var out bytes.Buffer
	if _, err := Run(stackUpEvent("frontend", "api"), src, &out); err != nil {
		t.Fatalf("Run(): %v\n%s", err, out.String())
	}

	env := map[string]string{}
	for _, line := range strings.Split(mustRead(t, filepath.Join(dir, "env.txt")), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	want := map[string]string{
		"DEVSTACK_HOOK_EVENT":   config.EventStackUp,
		"DEVSTACK_HOOK_NAME":    "context",
		"DEVSTACK_WORKSPACE":    "navexa",
		"DEVSTACK_STACK":        "feat",
		"DEVSTACK_STACK_BRANCH": "nick/feat",
		"DEVSTACK_ENV":          "dev",
		"DEVSTACK_SERVICES":     "api,frontend",
		"DEVSTACK_SERVICE":      "frontend",
		"DEVSTACK_SERVICE_PORT": "20006",
		"DEVSTACK_SERVICE_URL":  "http://localhost:20006",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestRunWritesPayloadToStdin(t *testing.T) {
	dir := t.TempDir()
	src := sourceWriting(t, dir, config.Hook{
		Name:     "payload",
		On:       []string{config.EventStackUp},
		Services: []string{"frontend"},
		Run:      "cat > payload.json",
	})

	var out bytes.Buffer
	if _, err := Run(stackUpEvent("frontend", "api"), src, &out); err != nil {
		t.Fatalf("Run(): %v\n%s", err, out.String())
	}

	var got payload
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(dir, "payload.json"))), &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if got.Event != config.EventStackUp || got.Hook != "payload" || got.Service != "frontend" {
		t.Errorf("payload identity = %+v", got)
	}
	if got.Stack["name"] != "feat" || got.Stack["isBase"] != false {
		t.Errorf("payload stack = %+v", got.Stack)
	}
	if got.Services["api"].Ports["http"] != 20005 {
		t.Errorf("payload should carry every service's ports, got %+v", got.Services)
	}
	if got.Services["frontend"].URL != "http://localhost:20006" {
		t.Errorf("frontend url = %q", got.Services["frontend"].URL)
	}
}

// A failing setup hook aborts: the stack is not silently left half-provisioned,
// and hooks after it do not run.
func TestRunAbortsOnSetupFailure(t *testing.T) {
	dir := t.TempDir()
	src := sourceWriting(t, dir,
		config.Hook{Name: "boom", On: []string{config.EventStackUp}, Run: "exit 3"},
		config.Hook{Name: "after", On: []string{config.EventStackUp}, Run: "touch after.txt"},
	)

	var out bytes.Buffer
	results, err := Run(stackUpEvent("frontend"), src, &out)
	if err == nil {
		t.Fatal("Run() = nil, want the failure to abort the event")
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("error = %v, want the exit status", err)
	}
	if len(results) != 1 || !results[0].Aborted {
		t.Fatalf("results = %#v, want one aborted result", results)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "after.txt")); statErr == nil {
		t.Error("hooks after an aborting failure must not run")
	}
}

// A failing teardown hook must not block teardown, or every broken hook leaks a
// worktree, a branch and a port.
func TestRunContinuesPastTeardownFailure(t *testing.T) {
	dir := t.TempDir()
	src := Source{
		WorkspaceRoot: dir,
		WorkspaceHooks: []config.Hook{
			{Name: "boom", On: []string{config.EventStackDestroy}, Run: "exit 1"},
			{Name: "after", On: []string{config.EventStackDestroy}, Run: "touch after.txt"},
		},
		Services: map[string]ServiceRef{"frontend": {Path: dir}},
	}

	ev := stackUpEvent("frontend")
	ev.Name = config.EventStackDestroy

	var out bytes.Buffer
	results, err := Run(ev, src, &out)
	if err != nil {
		t.Fatalf("Run() = %v, want teardown to survive a failing hook", err)
	}
	if len(results) != 2 || results[0].Err == nil || results[1].Err != nil {
		t.Fatalf("results = %#v", results)
	}
	if !strings.Contains(out.String(), "continuing") {
		t.Errorf("failure not reported to the user:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, "after.txt")); statErr != nil {
		t.Error("hooks after a continuing failure must still run")
	}
}

// onError overrides the event default in both directions.
func TestRunHonoursExplicitErrorPolicy(t *testing.T) {
	dir := t.TempDir()
	src := sourceWriting(t, dir, config.Hook{
		Name: "strict", On: []string{config.EventStackDestroy}, Run: "exit 1", OnError: config.OnErrorAbort,
	})
	ev := stackUpEvent("frontend")
	ev.Name = config.EventStackDestroy

	var out bytes.Buffer
	if _, err := Run(ev, src, &out); err == nil {
		t.Fatal("Run() = nil, want onError: abort to override the teardown default")
	}
}

func TestRunTimesOutARunawayHook(t *testing.T) {
	dir := t.TempDir()
	src := sourceWriting(t, dir, config.Hook{
		Name: "hang", On: []string{config.EventStackUp}, Run: "sleep 10", Timeout: "100ms",
	})

	var out bytes.Buffer
	_, err := Run(stackUpEvent("frontend"), src, &out)
	if err == nil || !strings.Contains(err.Error(), "timed out after 100ms") {
		t.Fatalf("Run() = %v, want a timeout error", err)
	}
}

func TestRunWithNoMatchingHooksDoesNothing(t *testing.T) {
	var out bytes.Buffer
	results, err := Run(stackUpEvent("frontend"), Source{}, &out)
	if err != nil || len(results) != 0 {
		t.Fatalf("Run() = %v, %#v", err, results)
	}
	if out.Len() != 0 {
		t.Errorf("silent path wrote output: %q", out.String())
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimRight(string(data), "\n")
}

// A failed stack.destroy hook is the one failure with no retry: the record its
// ${self...} references resolve against is deleted moments later. The output is
// the only surviving record of what needs cleaning by hand, so it has to carry
// the resolved values and say plainly that re-running is not an option.
func TestFailedDestroyHookPrintsUnretryableCleanupContext(t *testing.T) {
	dir := t.TempDir()
	src := Source{
		WorkspaceRoot:  dir,
		WorkspaceHooks: []config.Hook{{Name: "deprovision", On: []string{config.EventStackDestroy}, Run: "exit 1"}},
		Services:       map[string]ServiceRef{"frontend": {Path: dir}, "api": {Path: dir}},
	}
	ev := stackUpEvent("frontend", "api")
	ev.Name = config.EventStackDestroy

	var out bytes.Buffer
	if _, err := Run(ev, src, &out); err != nil {
		t.Fatalf("Run() = %v, want teardown to survive", err)
	}
	got := out.String()

	for _, want := range []string{
		"probably still there",
		"CANNOT be retried",
		"http://localhost:20006",
		"http://localhost:20005",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "devstack hooks run stack.destroy") {
		t.Errorf("must not offer a retry that cannot work:\n%s", got)
	}
}

// Every other teardown event keeps its record, so its failure names the retry.
func TestFailedStackDownHookOffersARetry(t *testing.T) {
	dir := t.TempDir()
	src := Source{
		WorkspaceRoot:  dir,
		WorkspaceHooks: []config.Hook{{Name: "detach", On: []string{config.EventStackDown}, Run: "exit 1"}},
		Services:       map[string]ServiceRef{"frontend": {Path: dir}},
	}
	ev := stackUpEvent("frontend")
	ev.Name = config.EventStackDown

	var out bytes.Buffer
	if _, err := Run(ev, src, &out); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "devstack hooks run stack.down --stack feat") {
		t.Errorf("output missing the retry command:\n%s", got)
	}
}

// A setup failure aborts and is returned as an error; it must not also print the
// teardown unwind advice, which would contradict it.
func TestSetupFailureDoesNotPrintUnwindHint(t *testing.T) {
	dir := t.TempDir()
	src := sourceWriting(t, dir, config.Hook{Name: "boom", On: []string{config.EventStackUp}, Run: "exit 1"})

	var out bytes.Buffer
	if _, err := Run(stackUpEvent("frontend"), src, &out); err == nil {
		t.Fatal("Run() = nil, want abort")
	}
	if strings.Contains(out.String(), "probably still there") {
		t.Errorf("setup failure printed teardown advice:\n%s", out.String())
	}
}
