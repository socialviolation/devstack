package cmd

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// withFreeHostPort points workspace.HostTiltPort at a port that nothing
// listens on. The real daemon of the machine holds the default port, and a test
// must never reach it.
func withFreeHostPort(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	previous := workspace.HostTiltPort
	workspace.HostTiltPort = port
	t.Cleanup(func() { workspace.HostTiltPort = previous })
}

// resourceWith builds one daemon resource with the runtime state and the
// disabled state a test needs.
func resourceWith(name, runtime string, disabled bool) tilt.UIResource {
	var r tilt.UIResource
	r.Metadata.Name = name
	r.Status.RuntimeStatus = runtime
	state := "Enabled"
	if disabled {
		state = "Disabled"
	}
	r.Status.DisableStatus = &tilt.DisableStatus{State: state}
	return r
}

// A user who stopped a service meant to stop it. An upgrade that started it
// again would undo a decision the user made, and would start a process nobody
// asked for.
func TestTheRestartStepTakesOnlyTheCopiesThatRun(t *testing.T) {
	view := &tilt.TiltView{UiResources: []tilt.UIResource{
		resourceWith("shop:api", "ok", false),
		resourceWith("shop:web", "none", false),
		resourceWith("shop:worker", "ok", true),
		resourceWith("shop:jobs", "pending", false),
	}}

	got := strings.Join(runningBaseCopies(view), ",")
	if got != "shop:api,shop:jobs" {
		t.Errorf("runningBaseCopies() = %q, want the two copies that run", got)
	}
}

// A feature stack runs out of its own worktree. An upgrade does not move that
// worktree, so restarting a stack copy interrupts somebody's work and gains
// nothing.
func TestTheRestartStepLeavesAStackCopyAlone(t *testing.T) {
	view := &tilt.TiltView{UiResources: []tilt.UIResource{
		resourceWith("shop:api", "ok", false),
		resourceWith("shop:api:feat", "ok", false),
	}}

	got := runningBaseCopies(view)
	if len(got) != 1 || got[0] != "shop:api" {
		t.Errorf("runningBaseCopies() = %v, want the base copy alone", got)
	}
}

// One service that will not come back must not strand the services after it.
// Each one is a separate process in a separate worktree, and the reader has to
// hear about every one of them.
func TestARestartFailureDoesNotStopTheCopiesAfterIt(t *testing.T) {
	var b strings.Builder
	names := []string{"shop:api", "shop:web", "shop:worker"}
	var tried []string

	errs := restartCopies(&b, names, func(name string) error {
		tried = append(tried, name)
		if name == "shop:web" {
			return fmt.Errorf("the daemon refused the trigger")
		}
		return nil
	})

	if strings.Join(tried, ",") != strings.Join(names, ",") {
		t.Fatalf("devstack tried %v, want every copy", tried)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "shop:web") {
		t.Fatalf("errs = %v, want the one failure, named", errs)
	}
	got := b.String()
	for _, want := range []string{"shop:api", "shop:web", "shop:worker", "FAILED", "the daemon refused the trigger"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report never states %q:\n%s", want, got)
		}
	}
}

// The report has to arrive as the work happens. A restart can take minutes,
// because each replica worktree is a new checkout that can need its own
// dependency install, and a reader who sees nothing until the end does not know
// whether devstack is working or stuck.
func TestTheRestartStepNamesEachCopyBeforeItRestartsIt(t *testing.T) {
	var b strings.Builder
	restartCopies(&b, []string{"shop:api"}, func(string) error {
		if got := b.String(); !strings.Contains(got, "shop:api") {
			t.Errorf("devstack restarts a copy it has not named yet:\n%s", got)
		}
		return nil
	})
	if !strings.Contains(b.String(), "restarted") {
		t.Errorf("the report never says the copy came back:\n%s", b.String())
	}
}

// A daemon that is not running has no running state to transform. devstack must
// say so and stop. Starting the daemon here would start every service that is
// set to start on its own, on a machine where nothing was running.
func TestTheTransformSaysSoWhenTheDaemonDoesNotRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withFreeHostPort(t)

	var b strings.Builder
	if err := transformRunningState(&b); err != nil {
		t.Fatalf("transformRunningState() = %v, want no error when there is no daemon", err)
	}
	got := b.String()
	if !strings.Contains(got, "does not run") || !strings.Contains(got, "starts no daemon") {
		t.Errorf("the report never says that there is nothing to transform:\n%s", got)
	}
}
