package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/stack"
)

func extendCmd() *cobra.Command {
	c := &cobra.Command{Use: "extend"}
	c.SetContext(context.Background())
	return c
}

func TestStackExtendPushesBackTheStopTimeAndPublishesTokens(t *testing.T) {
	rec, _ := buildStackScenario(t)
	viper.Set("workspace", "navexa")
	t.Cleanup(func() { viper.Set("workspace", "") })
	now := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	prevNow := stack.Now
	stack.Now = func() time.Time { return now }
	t.Cleanup(func() { stack.Now = prevNow })
	notified := 0
	prevNotify := herdr.Notify
	herdr.Notify = func() { notified++ }
	t.Cleanup(func() { herdr.Notify = prevNotify })

	out := captureStdout(t, func() {
		if err := runStackExtend(extendCmd(), []string{rec.Name, "2"}); err != nil {
			t.Fatalf("extend: %v", err)
		}
	})
	want := now.Add(2 * time.Hour)
	if !strings.Contains(out, want.Local().Format(time.RFC3339)) {
		t.Errorf("output = %q, want the new stop time %s", out, want.Local().Format(time.RFC3339))
	}
	got, err := stack.FindStack("navexa", rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !got.StopAt.Equal(want) {
		t.Errorf("StopAt = %v, want %v", got.StopAt, want)
	}
	if notified != 1 {
		t.Errorf("herdr.Notify ran %d times, want 1", notified)
	}

	if err := runStackExtend(extendCmd(), []string{rec.Name, "3"}); err != nil {
		t.Fatalf("second extend: %v", err)
	}
	got, _ = stack.FindStack("navexa", rec.Name)
	if !got.StopAt.Equal(want.Add(3 * time.Hour)) {
		t.Errorf("StopAt = %v, want the later stop time plus 3h", got.StopAt)
	}
}

func TestStackExtendRefusesBadHours(t *testing.T) {
	rec, _ := buildStackScenario(t)
	viper.Set("workspace", "navexa")
	t.Cleanup(func() { viper.Set("workspace", "") })

	for hours, want := range map[string]string{
		"25":  "24 hours or less",
		"0":   "more than 0",
		"two": "must be a number",
	} {
		err := runStackExtend(extendCmd(), []string{rec.Name, hours})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("hours %q: err = %v, want %q", hours, err, want)
		}
	}
}
