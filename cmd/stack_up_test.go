package cmd

import (
	"reflect"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
)

// A stack that folds into the host Tiltfile but never gets triggered sits at
// runtime "none" — up in name only. Every service it overlays must be in the
// list that gets enabled and triggered.
func TestStackStartOrderCoversEveryOverlayService(t *testing.T) {
	rec, _ := buildStackScenario(t)

	got := stack.StartOrder(rec)

	seen := map[string]bool{}
	for _, svc := range got {
		if seen[svc] {
			t.Fatalf("%q appears twice in %v — it would be triggered twice", svc, got)
		}
		seen[svc] = true
	}
	for _, svc := range rec.Overlay {
		if !seen[svc] {
			t.Errorf("overlay service %q missing from the start order %v, so it would never run", svc, got)
		}
	}
}

func TestStackStartOrderFallsBackToNameOrder(t *testing.T) {
	rec := &stack.Record{Name: "feat", Root: "/nonexistent", Overlay: []string{"web", "api"}}
	if got := stack.StartOrder(rec); !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Fatalf("start order with an unreadable stack manifest = %v, want [api web]", got)
	}
}

func TestStackPortLabelDropsTheDefaultHTTPKey(t *testing.T) {
	if got := stackPortLabel("navexa-api/http"); got != "navexa-api" {
		t.Errorf("stackPortLabel = %q, want navexa-api", got)
	}
	if got := stackPortLabel("navexa-api/grpc"); got != "navexa-api/grpc" {
		t.Errorf("portLabel dropped a key that tells two ports apart: %q", got)
	}
}
