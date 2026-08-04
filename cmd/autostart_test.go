package cmd

import (
	"errors"
	"strings"
	"testing"
)

// A daemon that will not come up is fatal: nothing can be triggered.
func TestAutoStartDaemonFailureIsFatal(t *testing.T) {
	fatal, warnings := autoStartOutcome(errors.New("port 10300 in use"), nil, []string{"api"})
	if fatal == nil {
		t.Fatal("autoStartOutcome() = nil, want a daemon failure to stop the start")
	}
	if !strings.Contains(fatal.Error(), "auto-start the dev daemon") {
		t.Errorf("error = %v, want it to name the daemon", fatal)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none when the daemon itself failed", warnings)
	}
}

// The bug: a workspace.up hook failure was wrapped as "failed to auto-start dev
// daemon" and returned, so a service whose daemon was up and healthy never
// started, and the message named a daemon problem that did not exist.
func TestAutoStartHookFailureIsNotFatalAndDoesNotBlameTheDaemon(t *testing.T) {
	fatal, warnings := autoStartOutcome(nil, errors.New(`hook "provision" failed on workspace.up: exit status 1`), []string{"api", "web"})
	if fatal != nil {
		t.Fatalf("autoStartOutcome() = %v, want the service start to proceed with the daemon up", fatal)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want the failure and the remedy", warnings)
	}
	joined := strings.Join(warnings, "\n")
	if strings.Contains(joined, "can not auto-start the dev daemon") {
		t.Errorf("a hook failure must not be reported as a daemon failure:\n%s", joined)
	}
	for _, want := range []string{"daemon is up", "workspace.up", "api, web", "devstack hooks run workspace.up"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q:\n%s", want, joined)
		}
	}
}

// The ordinary path says nothing.
func TestAutoStartSaysNothingWhenBothSucceed(t *testing.T) {
	fatal, warnings := autoStartOutcome(nil, nil, []string{"api"})
	if fatal != nil || len(warnings) != 0 {
		t.Fatalf("autoStartOutcome() = %v, %v; want silence", fatal, warnings)
	}
}

// A daemon failure takes precedence: the hooks never ran, so there is nothing
// to report about them.
func TestAutoStartDaemonFailureOutranksAnyHookResult(t *testing.T) {
	fatal, warnings := autoStartOutcome(errors.New("boom"), errors.New("hook also failed"), []string{"api"})
	if fatal == nil {
		t.Fatal("want the daemon failure returned")
	}
	if strings.Contains(strings.Join(warnings, "\n"), "hook also failed") {
		t.Error("a hook result must not be reported when the daemon never came up")
	}
}
