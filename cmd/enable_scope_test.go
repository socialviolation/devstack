package cmd

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// A dep that lives in base is not started by a --stack command, so it must not
// get a service.start hook either. Passing it on fired hooks for services this
// command left alone, and a failure there failed the whole start.
func TestServicesInBaseAreNotClaimedByAStackStart(t *testing.T) {
	present := map[string]bool{"navexa:api:feat": true}

	here, inBase := splitByPresence("navexa", "feat", "feat", []string{"api", "db", "cache"}, present)

	if !reflect.DeepEqual(here, []string{"api"}) {
		t.Errorf("triggered = %v, want only api", here)
	}
	if !reflect.DeepEqual(inBase, []string{"db", "cache"}) {
		t.Errorf("left in base = %v, want db and cache", inBase)
	}
}

// Without a stack every resolved service is this workspace's own, whatever the
// daemon view happens to hold at that moment.
func TestABaseStartClaimsEveryResolvedService(t *testing.T) {
	here, inBase := splitByPresence("navexa", "", "", []string{"api", "db"}, map[string]bool{})

	if !reflect.DeepEqual(here, []string{"api", "db"}) {
		t.Errorf("triggered = %v, want both", here)
	}
	if len(inBase) != 0 {
		t.Errorf("left in base = %v, want none", inBase)
	}
}

// funcBody returns the source of one top-level function in a cmd file.
func funcBody(t *testing.T, file, name string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(data)
	start := strings.Index(src, "\nfunc "+name+"(")
	if start < 0 {
		t.Fatalf("%s does not define %s", file, name)
	}
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		return src[start:]
	}
	return src[start : start+1+end]
}

// The hook set must be the services actually triggered, never the resolved set.
// The daemon call in between makes this unreachable from a unit test, so the
// wiring is pinned here.
func TestServiceStartHooksFireForTheTriggeredSet(t *testing.T) {
	body := funcBody(t, "enable_cmd.go", "runEnable")
	if !strings.Contains(body, "fireHooks(ws, stackName, config.EventServiceStart, here)") {
		t.Error("runEnable must fire service.start for `here`, the services it triggered")
	}
	if strings.Contains(body, "config.EventServiceStart, toTrigger") {
		t.Error("runEnable fires service.start for the resolved set, including deps it did not start")
	}
}

// A daemon failure and a hook failure must stay distinguishable, so the code
// that brings the daemon up fires nothing. Its caller decides what a hook
// failure means: fatal for 'workspace up', a warning for an auto-start.
func TestBringingTheDaemonUpFiresNoHooks(t *testing.T) {
	body := funcBody(t, "start_cmd.go", "bringWorkspaceUp")
	if strings.Contains(body, "fireHooks(") {
		t.Error("bringWorkspaceUp fires hooks, so a hook failure reads as a daemon failure again")
	}
	if !strings.Contains(funcBody(t, "start_cmd.go", "runStart"), "config.EventWorkspaceUp") {
		t.Error("runStart no longer fires workspace.up")
	}
}
