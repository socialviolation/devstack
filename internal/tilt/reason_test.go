package tilt

import (
	"encoding/json"
	"strings"
	"testing"
)

const waitingView = `{
 "uiSession": {"status": {"tiltfileKey": "/home/u/.local/share/devstack/_devstack-host/Tiltfile"}},
 "uiResources": [
  {"metadata": {"name": "navexa:nx-agent:agent-prod"},
   "status": {"runtimeStatus": "pending", "updateStatus": "pending",
    "disableStatus": {"enabledCount": 1, "disabledCount": 0, "state": "Enabled"},
    "waiting": {"reason": "waiting-for-dependencies",
     "on": [{"group": "tilt.dev", "apiVersion": "v1alpha1", "kind": "UIResource", "name": "navexa:agent-rag:agent-prod"}]}}},
  {"metadata": {"name": "navexa:agent-rag:agent-prod"},
   "status": {"runtimeStatus": "none", "updateStatus": "none",
    "disableStatus": {"enabledCount": 0, "disabledCount": 1, "state": "Disabled"}}},
  {"metadata": {"name": "navexa:navexa-api"},
   "status": {"runtimeStatus": "error", "updateStatus": "error",
    "buildHistory": [{"error": "exit status 1\nmore detail", "startTime": "2026-10-27T04:05:22Z", "finishTime": "2026-10-27T04:05:23Z"}],
    "disableStatus": {"state": "Enabled"}}}
 ]
}`

func parseView(t *testing.T) TiltView {
	t.Helper()
	var v TiltView
	if err := json.Unmarshal([]byte(waitingView), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func TestStateReasonNamesADisabledDependencyAndTheFix(t *testing.T) {
	v := parseView(t)
	got := StateReason(v.UiResources[0], v.UiResources)
	for _, want := range []string{
		"waiting-for-dependencies navexa:agent-rag:agent-prod (disabled)",
		"devstack service start agent-rag --stack agent-prod",
		"devstack dependencies remove nx-agent agent-rag",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("StateReason() = %q, want it to contain %q", got, want)
		}
	}
}

func TestStateReasonGivesTheFirstLineOfABuildError(t *testing.T) {
	v := parseView(t)
	if got := StateReason(v.UiResources[2], v.UiResources); got != "exit status 1" {
		t.Errorf("StateReason() = %q, want %q", got, "exit status 1")
	}
}

func TestStateReasonIsEmptyWithoutAReason(t *testing.T) {
	v := parseView(t)
	if got := StateReason(v.UiResources[1], v.UiResources); got != "" {
		t.Errorf("StateReason() = %q, want none", got)
	}
}

func TestViewCarriesTheLoadedTiltfile(t *testing.T) {
	v := parseView(t)
	if v.UISession == nil || v.UISession.Status.TiltfileKey != "/home/u/.local/share/devstack/_devstack-host/Tiltfile" {
		t.Errorf("UISession = %+v, want the tiltfileKey", v.UISession)
	}
}
