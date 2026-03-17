package tilt

import (
	"strings"
	"testing"
)

func TestGetView_TiltNotRunning(t *testing.T) {
	// Use a port that is almost certainly not in use.
	c := NewClient("localhost", 19999)
	view, err := c.GetView()
	if err == nil {
		t.Fatal("expected error when Tilt is not running, got nil")
	}
	if view != nil {
		t.Fatalf("expected nil view, got %+v", view)
	}
	if !strings.Contains(err.Error(), "Tilt is not running") {
		t.Fatalf("expected 'Tilt is not running' in error, got: %q", err.Error())
	}
	t.Logf("got expected error: %v", err)
}
