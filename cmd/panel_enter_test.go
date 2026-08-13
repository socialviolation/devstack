package cmd

import (
	"strings"
	"testing"
)

func TestPanelEnterSetting(t *testing.T) {
	for _, tc := range []struct {
		setting string
		copies  bool
	}{
		{"", false},
		{"open", false},
		{"copy", true},
		{" COPY ", true},
	} {
		copies, err := panelEnterCopies(tc.setting)
		if err != nil {
			t.Fatalf("panelEnterCopies(%q): %v", tc.setting, err)
		}
		if copies != tc.copies {
			t.Fatalf("panelEnterCopies(%q) = %v, want %v", tc.setting, copies, tc.copies)
		}
	}
}

func TestPanelEnterRejectsAnythingElseAndSaysHow(t *testing.T) {
	_, err := panelEnterCopies("paste")
	if err == nil {
		t.Fatal("expected an error for an unknown setting")
	}
	if !strings.Contains(err.Error(), "--enter copy") {
		t.Fatalf("the error does not say how to set it: %v", err)
	}
}
