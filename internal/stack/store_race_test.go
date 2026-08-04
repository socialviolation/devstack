package stack

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
)

func seedRace(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := saveStore("navexa", []Record{{Name: "perf", Base: "navexa", Note: "NAV-412 daily value spike"}}); err != nil {
		t.Fatalf("saveStore: %v", err)
	}
}

func TestAppendNoteConcurrentKeepsEveryEntry(t *testing.T) {
	seedRace(t)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	texts := []string{"cache warms on boot", "spike is in the FX join"}
	appended := make([]bool, len(texts))
	errs := make([]error, len(texts))
	for i, text := range texts {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			appended[i], _, errs[i] = AppendNote("navexa", "perf", text)
		}()
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("AppendNote(%q) = %v", texts[i], err)
		}
	}
	log := logOf(t, "perf")
	if len(log) != 2 {
		t.Fatalf("Log holds %d entries, want 2: both calls reported appended=%v and neither may be lost. Log = %v", len(log), appended, log)
	}
}

func TestCreateConcurrentWithNoteKeepsBoth(t *testing.T) {
	seedRace(t)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(2)
	var noteErr, addErr error
	go func() {
		defer done.Done()
		start.Wait()
		_, _, noteErr = AppendNote("navexa", "perf", "cache warms on boot")
	}()
	go func() {
		defer done.Done()
		start.Wait()
		addErr = upsertStack(Record{Name: "import", Base: "navexa"})
	}()
	start.Done()
	done.Wait()

	if noteErr != nil {
		t.Fatalf("AppendNote: %v", noteErr)
	}
	if addErr != nil {
		t.Fatalf("upsertStack: %v", addErr)
	}
	recs, err := LoadStore("navexa")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("the store holds %d stacks, want 2: the create and the note must both survive. Store = %v", len(recs), recs)
	}
	if len(logOf(t, "perf")) != 1 {
		t.Fatal("the note entry is gone: the create erased it")
	}
}

func TestRemoveConcurrentWithActivateDoesNotResurrect(t *testing.T) {
	seedRace(t)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(2)
	var removed bool
	var rmErr, upErr error
	go func() {
		defer done.Done()
		start.Wait()
		removed, rmErr = deleteStack("navexa", "perf")
	}()
	go func() {
		defer done.Done()
		start.Wait()
		upErr = SetActive("navexa", "perf", true)
	}()
	start.Done()
	done.Wait()

	if rmErr != nil {
		t.Fatalf("deleteStack: %v", rmErr)
	}
	if !removed {
		t.Fatal("deleteStack reported nothing removed, want the stack removed")
	}
	recs, err := LoadStore("navexa")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("the removed stack is back in the store: %v (SetActive returned %v)", recs, upErr)
	}
}

func TestSaveStoreIsAtomicForConcurrentReaders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	big := make([]Record, 0, 200)
	for i := 0; i < 200; i++ {
		big = append(big, Record{Name: "perf", Base: "navexa", Note: "NAV-412 daily value spike, which is long enough to make the file large"})
	}
	if err := saveStore("navexa", big); err != nil {
		t.Fatalf("saveStore: %v", err)
	}

	stop := make(chan struct{})
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = saveStore("navexa", big)
		}
	}()

	bad := 0
	for i := 0; i < 2000; i++ {
		data, err := os.ReadFile(storePath("navexa"))
		if err != nil {
			continue
		}
		var recs []Record
		if json.Unmarshal(data, &recs) != nil {
			bad++
		}
	}
	close(stop)
	done.Wait()

	if bad > 0 {
		t.Fatalf("%d of 2000 reads got a half-written store, want 0", bad)
	}
}
