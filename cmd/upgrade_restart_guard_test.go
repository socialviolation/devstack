package cmd

import (
	"fmt"
	"strings"
	"testing"
)

// A restart after step 2 failed stops a service that serves now and starts it
// again out of the checkout, because the replica it needs was never built.
func TestTheTransformStepStopsWhenTheMigrationFailed(t *testing.T) {
	out := captureStdout(t, func() {
		if err := transformStep(false, false, fmt.Errorf("navexa: the replica did not build")); err != nil {
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
		if err := transformStep(false, false, nil); err != nil {
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
	})
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
