package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/tilt"
)

// A restart after step 2 failed stops a service that serves now and starts it
// again out of the checkout, because the replica it needs was never built.
func TestTheTransformStepStopsWhenTheMigrationFailed(t *testing.T) {
	out := captureStdout(t, func() {
		if err := transformStep(false, false, step2Result{MigrateErr: fmt.Errorf("navexa: the migration did not finish")}); err != nil {
			t.Fatalf("transformStep() = %v, want no second error", err)
		}
	})
	if !strings.Contains(out, "STEP 2 FAILED") {
		t.Errorf("the report never names the failure of step 2:\n%s", out)
	}
	if !strings.Contains(out, "restarts nothing") {
		t.Errorf("the report never says that devstack restarts nothing:\n%s", out)
	}
}

func TestTheTransformStepRunsWhenTheMigrationSucceeded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withFreeHostPort(t)

	out := captureStdout(t, func() {
		if err := transformStep(false, false, step2Result{}); err != nil {
			t.Fatalf("transformStep() = %v", err)
		}
	})
	if strings.Contains(out, "STEP 2 FAILED") {
		t.Errorf("the report names a failure that did not happen:\n%s", out)
	}
	if !strings.Contains(out, "does not run") {
		t.Errorf("the transform did not run:\n%s", out)
	}
}

// "Each one serves the replica now" is false for a workspace whose replica
// devstack can not read: generation falls back to the checkout, and the copy
// runs the work parked there.
func TestTheRestartReportNamesTheWorkspaceThatServesTheCheckout(t *testing.T) {
	var b strings.Builder
	writeRestartReport(&b, []string{"shop:api", "shop:web", "navexa:backend"}, map[string]bool{
		"shop":   true,
		"navexa": false,
	}, nil)
	got := b.String()

	if !strings.Contains(got, "shop") || !strings.Contains(got, "2 copies. Each one serves the replica now.") {
		t.Errorf("the report does not say that shop serves the replica:\n%s", got)
	}
	if !strings.Contains(got, "navexa") || !strings.Contains(got, "serves your checkout") {
		t.Errorf("the report claims navexa serves the replica:\n%s", got)
	}
	shop := strings.Index(got, "shop")
	navexa := strings.Index(got, "navexa")
	if navexa > shop {
		t.Errorf("the workspaces are not in name order:\n%s", got)
	}
}

// A migration that refused wrote nothing, and it stopped nothing. Step 2 still
// built each replica, so each copy has a replica to serve. An upgrade that
// installed a new binary and then left every copy on the old code is half done,
// so the restart goes ahead, and the report carries the refusal to the reader.
func TestTheTransformStepRunsWhenTheMigrationOnlyRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withFreeHostPort(t)

	out := captureStdout(t, func() {
		if err := transformStep(false, false, step2Result{MigrateErr: migrate.ErrRefused}); err != nil {
			t.Fatalf("transformStep() = %v", err)
		}
	})
	if strings.Contains(out, "STEP 2 FAILED") {
		t.Errorf("the report calls a refusal a failure:\n%s", out)
	}
	for _, want := range []string{
		"REFUSED TO MIGRATE",
		"changed no file",
		"nobody committed",
		"devstack migrate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report never states %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "does not run") {
		t.Errorf("the transform did not run:\n%s", out)
	}
}

// devstack knows nothing about any replica when it can not read the registry.
// The report used to fall back to an empty map, which reads as "no workspace has
// a readable replica", so every restarted workspace was told that its copies
// serve the checkout. devstack had not looked at one of them.
func TestTheRestartReportStatesARegistryFailureOnce(t *testing.T) {
	var b strings.Builder
	writeRestartReport(&b, []string{"shop:api", "navexa:backend"}, nil,
		fmt.Errorf("open workspaces.json: permission denied"))
	got := b.String()

	if strings.Contains(got, "serves your checkout") {
		t.Errorf("the report claims something about a workspace it never read:\n%s", got)
	}
	if strings.Count(got, "can not read the workspace registry") != 1 {
		t.Errorf("the registry failure is not stated exactly one time:\n%s", got)
	}
	for _, want := range []string{"permission denied", "restarted 2 copies", "devstack status --all"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report never states %q:\n%s", want, got)
		}
	}
}

// One workspace whose replica did not build must not keep the whole machine on
// the old code. Its own copies have no replica to serve, and every other
// workspace has one.
func TestTheRestartSkipsOnlyTheWorkspaceWhoseReplicaDidNotBuild(t *testing.T) {
	view := &tilt.TiltView{UiResources: []tilt.UIResource{
		resourceWith("shop:api", "ok", false),
		resourceWith("navexa:backend", "ok", false),
		resourceWith("navexa:web", "ok", false),
	}}

	got := strings.Join(runningBaseCopies(view, "", []string{"navexa"}), ",")
	if got != "shop:api" {
		t.Fatalf("runningBaseCopies() = %q, want the copies of every workspace that has a replica", got)
	}
	if all := strings.Join(runningBaseCopies(view, "", nil), ","); all != "navexa:backend,navexa:web,shop:api" {
		t.Fatalf("with nothing skipped, runningBaseCopies() = %q, want every running base copy", all)
	}
}

// The reader has to hear which workspaces devstack left alone, and why. "A
// restart now would move a copy onto your checkout" was printed for the whole
// machine, and it was true of the one workspace that failed.
func TestTheTransformStepNamesTheWorkspacesItSkips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withFreeHostPort(t)

	out := captureStdout(t, func() {
		if err := transformStep(false, false, step2Result{
			ReplicaErr: fmt.Errorf("navexa: the replica did not build"),
			Unbuilt:    []string{"navexa"},
		}); err != nil {
			t.Fatalf("transformStep() = %v", err)
		}
	})

	if strings.Contains(out, "STEP 2 FAILED") {
		t.Errorf("one workspace's replica stopped step 3 for the machine:\n%s", out)
	}
	for _, want := range []string{"the workspace navexa", "did not build", "devstack workspace up"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report never states %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "does not run") {
		t.Errorf("the transform did not run for the workspaces that do have a replica:\n%s", out)
	}
}
