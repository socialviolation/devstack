package tunnel

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/tilt"
)

func TestDiscover(t *testing.T) {
	view := &tilt.TiltView{UiResources: []tilt.UIResource{
		res("navexa:api", "ok", "http://localhost:8080"),
		res("navexa:frontend", "ok", "http://localhost:4200", "http://localhost:4200/health"), // dup port
		res("navexa:api:perf", "ok", "http://localhost:8090"),
		res("other:api", "ok", "http://localhost:9090"),
	}}

	names := func(svcs []Service) map[string]int {
		m := map[string]int{}
		for _, s := range svcs {
			m[s.Name] = s.Port
		}
		return m
	}

	t.Run("base only, own workspace", func(t *testing.T) {
		got := names(Discover(view, nil, "navexa", false))
		want := map[string]int{"api": 8080, "frontend": 4200}
		if len(got) != len(want) {
			t.Fatalf("want %v, got %v", want, got)
		}
		for n, p := range want {
			if got[n] != p {
				t.Errorf("want %s:%d, got %d", n, p, got[n])
			}
		}
	})

	t.Run("includeStacks adds stack resources", func(t *testing.T) {
		got := names(Discover(view, nil, "navexa", true))
		want := map[string]int{"api": 8080, "frontend": 4200, "api:perf": 8090}
		if len(got) != len(want) {
			t.Fatalf("want %v, got %v", want, got)
		}
		for n, p := range want {
			if got[n] != p {
				t.Errorf("want %s:%d, got %d", n, p, got[n])
			}
		}
	})

	t.Run("other workspace never included", func(t *testing.T) {
		for _, includeStacks := range []bool{false, true} {
			for _, s := range Discover(view, nil, "navexa", includeStacks) {
				if s.Port == 9090 {
					t.Fatalf("other:api leaked (includeStacks=%v): %+v", includeStacks, s)
				}
			}
		}
	})

	t.Run("filter matches bare service name across base and stack", func(t *testing.T) {
		got := names(Discover(view, map[string]bool{"api": true}, "navexa", true))
		want := map[string]int{"api": 8080, "api:perf": 8090}
		if len(got) != len(want) {
			t.Fatalf("want %v, got %v", want, got)
		}
		for n, p := range want {
			if got[n] != p {
				t.Errorf("want %s:%d, got %d", n, p, got[n])
			}
		}
	})
}

func res(name, runtime string, urls ...string) tilt.UIResource {
	var r tilt.UIResource
	r.Metadata.Name = name
	r.Status.RuntimeStatus = runtime
	for _, u := range urls {
		r.Status.EndpointLinks = append(r.Status.EndpointLinks, tilt.EndpointLink{URL: u})
	}
	return r
}

func TestPartitionServing(t *testing.T) {
	// Bind a real listener so exactly one port is "serving".
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	live := ln.Addr().(*net.TCPAddr).Port

	svcs := []Service{
		{Name: "up", Port: live},
		{Name: "down", Port: 1}, // port 1 is not bound
	}
	serving, idle := PartitionServing(svcs)
	if len(serving) != 1 || serving[0].Name != "up" {
		t.Fatalf("serving: want [up], got %+v", serving)
	}
	if len(idle) != 1 || idle[0].Name != "down" {
		t.Fatalf("idle: want [down], got %+v", idle)
	}
}

func TestCheckConnectivity(t *testing.T) {
	dir := t.TempDir()
	orig := sshBin
	defer func() { sshBin = orig }()

	t.Run("ok", func(t *testing.T) {
		stub := filepath.Join(dir, "ssh-ok")
		mustWrite(t, stub, "#!/bin/sh\nexit 0\n")
		sshBin = stub
		if err := CheckConnectivity("user", "host"); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("auth failure carries ssh message", func(t *testing.T) {
		stub := filepath.Join(dir, "ssh-fail")
		mustWrite(t, stub, "#!/bin/sh\necho 'user@host: Permission denied (publickey).' >&2\nexit 255\n")
		sshBin = stub
		err := CheckConnectivity("user", "host")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "Permission denied") {
			t.Fatalf("error should carry ssh message, got %q", err.Error())
		}
	})
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

// TestSleepHelper is inert unless re-executed by fakeForward.
func TestSleepHelper(t *testing.T) {
	if os.Getenv("DEVSTACK_TEST_SLEEP") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}

// fakeForward spawns a live process whose /proc cmdline reads as
// `ssh ... -L p:localhost:p`, which is what strayForwards matches on. Args[0]
// is set independently of Path so the test binary presents as ssh, and the ssh
// flags sit behind -- so the binary's own flag parser ignores them.
func fakeForward(t *testing.T, port int) int {
	t.Helper()
	fwd := fmt.Sprintf("%d:localhost:%d", port, port)
	cmd := exec.Command(os.Args[0])
	cmd.Path = os.Args[0]
	cmd.Args = []string{"ssh", "-test.run=TestSleepHelper", "--", "-N", "-L", fwd, "user@host"}
	cmd.Env = append(os.Environ(), "DEVSTACK_TEST_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn fake forward: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func writePID(t *testing.T, wsName string, port, pid int) {
	t.Helper()
	if err := os.MkdirAll(Dir(wsName), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile(wsName, port), []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestTrackedForwardsSpansWorkspaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	writePID(t, "ws-a", 5432, 111)
	writePID(t, "ws-b", 6379, 222)

	owned := trackedForwards()
	for _, pid := range []int{111, 222} {
		if !owned[pid] {
			t.Errorf("pid %d from another workspace should be tracked; got %v", pid, owned)
		}
	}
}

func TestStrayForwardsSparesOtherWorkspaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const port = 59322

	pid := fakeForward(t, port)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !matchesFwd(pid, port) {
		time.Sleep(50 * time.Millisecond)
	}
	if !matchesFwd(pid, port) {
		t.Fatalf("fake forward %d never presented an ssh cmdline", pid)
	}

	if got := strayForwards(port); !contains(got, pid) {
		t.Fatalf("untracked forward %d should be stray; got %v", pid, got)
	}

	writePID(t, "ws-other", port, pid)
	if got := strayForwards(port); contains(got, pid) {
		t.Fatalf("forward %d tracked by ws-other must not be stray; got %v", pid, got)
	}

	KillPort("ws-mine", port)
	if !Alive(pid) {
		t.Fatal("KillPort from ws-mine killed a forward owned by ws-other")
	}
}

func matchesFwd(pid, port int) bool {
	return contains(strayForwards(port), pid) || trackedForwards()[pid]
}

func contains(pids []int, want int) bool {
	for _, p := range pids {
		if p == want {
			return true
		}
	}
	return false
}

// TestLaunchLifecycle verifies Launch spawns a tracked, detached process and
// KillPort tears it down and clears the PID file. A stub stands in for ssh.
func TestLaunchLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // workspace.DataDir resolves under $HOME

	stub := filepath.Join(dir, "ssh-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	orig := sshBin
	sshBin = stub
	defer func() { sshBin = orig }()

	const port = 59321
	pid, err := Launch("testws", ModePull, "user", "host", port)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !Alive(pid) {
		t.Fatalf("process %d not alive after launch", pid)
	}
	if !IsUp("testws", port) {
		t.Fatal("IsUp false right after launch")
	}
	if PID("testws", port) != pid {
		t.Fatalf("PID file mismatch: got %d want %d", PID("testws", port), pid)
	}

	KillPort("testws", port)

	// Give the SIGTERM a moment to land.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && Alive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if Alive(pid) {
		t.Fatalf("process %d still alive after KillPort", pid)
	}
	if IsUp("testws", port) {
		t.Fatal("IsUp true after KillPort")
	}
	if _, err := os.Stat(pidFile("testws", port)); !os.IsNotExist(err) {
		t.Fatalf("PID file not removed after KillPort: %v", err)
	}
}

// A forward outlives whatever created it, so stopping and reporting must work
// from the PID files rather than from service discovery — otherwise a forward
// for something no longer discoverable (the observability UI, a removed
// service) is left running with no way to reach it.
func TestTrackedPortsReadsLiveForwards(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := Dir("navexa")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"5080.pid", "63290.pid", "20000.pid", "notes.txt", "bogus.pid"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("1234"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got := TrackedPorts("navexa")
	want := []int{5080, 20000, 63290}
	if len(got) != len(want) {
		t.Fatalf("TrackedPorts() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TrackedPorts() = %v, want %v (sorted)", got, want)
		}
	}
}

func TestTrackedPortsNoForwards(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := TrackedPorts("navexa"); len(got) != 0 {
		t.Errorf("TrackedPorts() = %v, want none", got)
	}
}
