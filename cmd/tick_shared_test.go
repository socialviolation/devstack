package cmd

import (
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestTickSyncsTheSharedSetAtMostOnceAnHour(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := 0
	prev := syncAllShared
	syncAllShared = func(io.Writer) error { n++; return nil }
	t.Cleanup(func() { syncAllShared = prev })

	for i := 0; i < 3; i++ {
		if err := tickSharedSync(); err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 {
		t.Fatalf("three ticks in a row synced %d times, want 1", n)
	}

	old := time.Now().Add(-59 * time.Minute).Format(time.RFC3339Nano)
	if err := os.WriteFile(sharedSyncStampPath(), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	if err := tickSharedSync(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("a tick 59 minutes after the last sync synced again, want it throttled")
	}

	old = time.Now().Add(-61 * time.Minute).Format(time.RFC3339Nano)
	if err := os.WriteFile(sharedSyncStampPath(), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	if err := tickSharedSync(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("a tick 61 minutes after the last sync synced %d times in total, want 2", n)
	}
}

func TestTickRunsTheSharedSync(t *testing.T) {
	found := false
	for _, job := range tickJobs {
		if funcName(job) == funcName(tickSharedSync) {
			found = true
		}
	}
	if !found {
		t.Error("tickJobs does not run tickSharedSync, so the shared set never follows main")
	}
}

func funcName(f func() error) string {
	return runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
}

func TestTickRetriesAFailedSharedSyncOnTheNextTick(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := 0
	prev := syncAllShared
	syncAllShared = func(io.Writer) error {
		n++
		if n == 1 {
			return errors.New("git fetch failed")
		}
		return nil
	}
	t.Cleanup(func() { syncAllShared = prev })

	if err := tickSharedSync(); err == nil {
		t.Fatal("a failed sync returned no error")
	}
	if err := tickSharedSync(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("the tick after a failed sync synced %d times in total, want 2", n)
	}
}
