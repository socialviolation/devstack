package tunnel

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/tilt"
)

func TestDiscover(t *testing.T) {
	view := &tilt.TiltView{UiResources: []tilt.UIResource{
		res("api", "ok", "http://localhost:8080"),
		res("frontend", "ok", "http://localhost:4200", "http://localhost:4200/health"), // dup port
		res("worker", "pending"),                                                       // no endpoints
		res("db", "ok", "http://localhost:5432"),
	}}

	t.Run("all", func(t *testing.T) {
		got := Discover(view, nil)
		if len(got) != 3 {
			t.Fatalf("want 3 services (deduped), got %d: %+v", len(got), got)
		}
		ports := map[int]bool{}
		for _, s := range got {
			ports[s.Port] = true
		}
		for _, want := range []int{8080, 4200, 5432} {
			if !ports[want] {
				t.Errorf("missing port %d", want)
			}
		}
	})

	t.Run("filter", func(t *testing.T) {
		got := Discover(view, map[string]bool{"api": true})
		if len(got) != 1 || got[0].Port != 8080 {
			t.Fatalf("want only api:8080, got %+v", got)
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
