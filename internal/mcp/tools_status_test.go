package mcp

import (
	"testing"

	"github.com/socialviolation/devstack/internal/tilt"
)

func TestMCPServiceStatusMatrix(t *testing.T) {
	cases := []struct {
		name     string
		runtime  string
		update   string
		disabled bool
		want     string
	}{
		{name: "runtime ok", runtime: "ok", want: "running"},
		{name: "runtime pending", runtime: "pending", want: "starting"},
		{name: "runtime error", runtime: "error", want: "erroring"},
		{name: "update running", runtime: "none", update: "running", want: "building"},
		{name: "update error", runtime: "none", update: "error", want: "erroring"},
		{name: "nothing happening", runtime: "none", update: "none", want: "stopped"},
		{name: "disabled beats running", runtime: "ok", disabled: true, want: "disabled"},
		{name: "disabled beats erroring", runtime: "error", disabled: true, want: "disabled"},
		{name: "disabled beats building", runtime: "none", update: "running", disabled: true, want: "disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r tilt.UIResource
			r.Status.RuntimeStatus = tc.runtime
			r.Status.UpdateStatus = tc.update
			if tc.disabled {
				r.Status.DisableStatus = &tilt.DisableStatus{State: "Disabled"}
			}
			if got := mcpServiceStatus(r); got != tc.want {
				t.Fatalf("mcpServiceStatus(runtime=%q update=%q disabled=%v) = %q, want %q", tc.runtime, tc.update, tc.disabled, got, tc.want)
			}
		})
	}
}
