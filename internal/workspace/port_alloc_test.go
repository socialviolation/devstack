package workspace

import (
	"fmt"
	"net"
	"sync"
	"testing"
)

func TestAllocatePortsDistinctAndPersisted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	keys := []string{"http", "grpc", "admin", "metrics"}
	ports, err := AllocatePorts("navexa--a", keys)
	if err != nil {
		t.Fatalf("AllocatePorts(): %v", err)
	}
	if len(ports) != len(keys) {
		t.Fatalf("got %d ports, want %d", len(ports), len(keys))
	}
	seen := map[int]bool{}
	for _, k := range keys {
		p, ok := ports[k]
		if !ok {
			t.Fatalf("key %q missing from allocation", k)
		}
		if seen[p] {
			t.Fatalf("port %d allocated twice", p)
		}
		seen[p] = true
	}

	loaded, err := LoadPorts("navexa--a")
	if err != nil {
		t.Fatalf("LoadPorts(): %v", err)
	}
	if len(loaded) != len(ports) {
		t.Fatalf("LoadPorts() = %v, want %v", loaded, ports)
	}
	for k, v := range ports {
		if loaded[k] != v {
			t.Fatalf("LoadPorts()[%q] = %d, want %d", k, loaded[k], v)
		}
	}
}

func TestAllocatePortsExcludesOtherStacks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a, err := AllocatePorts("navexa--a", []string{"http", "grpc", "admin"})
	if err != nil {
		t.Fatalf("AllocatePorts(a): %v", err)
	}
	b, err := AllocatePorts("navexa--b", []string{"http", "grpc", "admin"})
	if err != nil {
		t.Fatalf("AllocatePorts(b): %v", err)
	}

	held := map[int]bool{}
	for _, p := range a {
		held[p] = true
	}
	for k, p := range b {
		if held[p] {
			t.Fatalf("stack B key %q got port %d already held by stack A", k, p)
		}
	}
}

func TestAllocatePortsSkipsRegisteredTiltPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := Register(Workspace{Name: "navexa", Path: "/tmp/navexa", TiltPort: servicePortBase}); err != nil {
		t.Fatalf("Register(): %v", err)
	}

	ports, err := AllocatePorts("navexa--a", []string{"http"})
	if err != nil {
		t.Fatalf("AllocatePorts(): %v", err)
	}
	if ports["http"] == servicePortBase {
		t.Fatalf("allocation returned registered TiltPort %d", servicePortBase)
	}
}

func TestAllocatePortsSkipsListeningPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ln, blocked := listenInRange(t)
	defer ln.Close()

	ports, err := AllocatePorts("navexa--a", []string{"http"})
	if err != nil {
		t.Fatalf("AllocatePorts(): %v", err)
	}
	if ports["http"] == blocked {
		t.Fatalf("allocation returned listening port %d", blocked)
	}
}

func TestAllocatePortsConcurrentGloballyDistinct(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const stacks = 12
	const perStack = 4

	var wg sync.WaitGroup
	results := make([]map[string]int, stacks)
	errs := make([]error, stacks)
	for i := 0; i < stacks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			keys := make([]string, perStack)
			for k := range keys {
				keys[k] = fmt.Sprintf("svc%d", k)
			}
			results[i], errs[i] = AllocatePorts(fmt.Sprintf("stack-%d", i), keys)
		}(i)
	}
	wg.Wait()

	seen := map[int]int{}
	for i := 0; i < stacks; i++ {
		if errs[i] != nil {
			t.Fatalf("AllocatePorts(stack-%d): %v", i, errs[i])
		}
		if len(results[i]) != perStack {
			t.Fatalf("stack-%d got %d ports, want %d", i, len(results[i]), perStack)
		}
		for _, p := range results[i] {
			if prev, ok := seen[p]; ok {
				t.Fatalf("port %d handed to stack-%d and stack-%d", p, prev, i)
			}
			seen[p] = i
		}
	}
	if len(seen) != stacks*perStack {
		t.Fatalf("got %d distinct ports, want %d", len(seen), stacks*perStack)
	}
}

func TestReleasePortsFreesAndReallocates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := AllocatePorts("navexa--a", []string{"http", "grpc"})
	if err != nil {
		t.Fatalf("AllocatePorts(): %v", err)
	}

	if err := ReleasePorts("navexa--a"); err != nil {
		t.Fatalf("ReleasePorts(): %v", err)
	}
	loaded, err := LoadPorts("navexa--a")
	if err != nil {
		t.Fatalf("LoadPorts(): %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("LoadPorts() after release = %v, want empty", loaded)
	}

	second, err := AllocatePorts("navexa--b", []string{"http", "grpc"})
	if err != nil {
		t.Fatalf("AllocatePorts(): %v", err)
	}
	freed := map[int]bool{}
	for _, p := range first {
		freed[p] = true
	}
	reused := false
	for _, p := range second {
		if freed[p] {
			reused = true
		}
	}
	if !reused {
		t.Fatalf("released ports %v were not reallocatable (got %v)", first, second)
	}
}

func TestAllocatePortsExhaustedRange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	orig := portInUse
	portInUse = func(int) bool { return true }
	defer func() { portInUse = orig }()

	_, err := AllocatePorts("navexa--a", []string{"http"})
	if err == nil {
		t.Fatalf("AllocatePorts() = nil error, want exhaustion error")
	}
}

// listenInRange binds the first free port at or above servicePortBase so the
// listening-exclusion test has a known port the real dial probe must reject.
func listenInRange(t *testing.T) (net.Listener, int) {
	t.Helper()
	for p := servicePortBase; p < servicePortBase+servicePortScanLimit; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			return ln, p
		}
	}
	t.Fatalf("no bindable port in service range")
	return nil, 0
}
