package workspace

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
)

func seedRegistryRace(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := Save([]Workspace{
		{Name: "navexa", Path: home + "/dev/navexa", TiltPort: 10350},
		{Name: "tsfc", Path: home + "/dev/tsfc", TiltPort: 10351},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return home
}

func namesOf(t *testing.T) []string {
	t.Helper()
	all, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var names []string
	for _, ws := range all {
		names = append(names, ws.Name)
	}
	return names
}

func has(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// `workspace rm` read the registry, dropped one entry, and wrote the whole file
// back, all outside the lock. A `workspace add` that ran beside it wrote the
// entries it had read, which still held the removed workspace, so the removal
// came back and the command that reported it said nothing.
func TestRemoveConcurrentWithAddStaysRemoved(t *testing.T) {
	home := seedRegistryRace(t)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(2)
	var rmErr, addErr error
	go func() {
		defer done.Done()
		start.Wait()
		_, rmErr = Deregister("navexa")
	}()
	go func() {
		defer done.Done()
		start.Wait()
		addErr = Register(Workspace{Name: "shop", Path: home + "/dev/shop"})
	}()
	start.Done()
	done.Wait()

	if rmErr != nil {
		t.Fatalf("Deregister: %v", rmErr)
	}
	if addErr != nil {
		t.Fatalf("Register: %v", addErr)
	}
	names := namesOf(t)
	if has(names, "navexa") {
		t.Fatalf("the removed workspace is back in the registry: %v", names)
	}
	if !has(names, "shop") {
		t.Fatalf("the added workspace is gone from the registry: %v", names)
	}
}

// Every writer of the registry must hold the lock, not only the two that took
// it. An update that writes the whole file back erases an entry added beside it
// exactly as a removal does.
func TestUpdateConcurrentWithAddKeepsBoth(t *testing.T) {
	home := seedRegistryRace(t)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(2)
	var portErr, addErr error
	go func() {
		defer done.Done()
		start.Wait()
		portErr = UpdatePort("navexa", 10360)
	}()
	go func() {
		defer done.Done()
		start.Wait()
		addErr = Register(Workspace{Name: "shop", Path: home + "/dev/shop"})
	}()
	start.Done()
	done.Wait()

	if portErr != nil {
		t.Fatalf("UpdatePort: %v", portErr)
	}
	if addErr != nil {
		t.Fatalf("Register: %v", addErr)
	}
	names := namesOf(t)
	if !has(names, "shop") {
		t.Fatalf("the added workspace is gone from the registry: %v", names)
	}
	found, err := FindByName("navexa")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	if found.TiltPort != 10360 {
		t.Fatalf("TiltPort = %d, want the updated 10360: the add erased the update", found.TiltPort)
	}
}

func TestSaveIsAtomicForConcurrentReaders(t *testing.T) {
	home := seedRegistryRace(t)
	big := make([]Workspace, 0, 200)
	for i := 0; i < 200; i++ {
		big = append(big, Workspace{Name: "navexa", Path: home + "/dev/navexa", TiltPort: 10350,
			OtelPluginConfig: map[string]string{"upstream": "https://otel.example.com:4318/v1/traces"}})
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
			_ = Save(big)
		}
	}()

	bad := 0
	for i := 0; i < 2000; i++ {
		data, err := os.ReadFile(RegistryPath())
		if err != nil {
			continue
		}
		var all []Workspace
		if json.Unmarshal(data, &all) != nil {
			bad++
		}
	}
	close(stop)
	done.Wait()

	if bad > 0 {
		t.Fatalf("%d of 2000 reads got a half-written registry, want 0", bad)
	}
}
