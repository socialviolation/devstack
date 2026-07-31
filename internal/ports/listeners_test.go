package ports

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// tcpTable renders a /proc/net/tcp-shaped table. state is the hex socket state:
// "0A" is LISTEN, "01" is ESTABLISHED.
func tcpTable(rows ...[3]string) string {
	out := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	for i, r := range rows {
		out += fmt.Sprintf("   %d: %s 00000000:0000 %s 00000000:00000000 00:00000000 00000000  1000        0 %s 1 0000000000000000 100 0 0 10 0\n",
			i, r[0], r[1], r[2])
	}
	return out
}

// fakeProc builds a /proc tree: socket tables plus one directory per pid whose
// fd links point at the given socket inodes.
func fakeProc(t *testing.T, tcp, tcp6 string, fds map[int][]string, cmdlines map[int]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"net/tcp": tcp, "net/tcp6": tcp6} {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for pid, inodes := range fds {
		fdDir := filepath.Join(root, fmt.Sprint(pid), "fd")
		if err := os.MkdirAll(fdDir, 0755); err != nil {
			t.Fatal(err)
		}
		for i, inode := range inodes {
			// A real /proc fd is a symlink reading "socket:[<inode>]". The target
			// need not exist, which is what lets this be faked at all.
			if err := os.Symlink("socket:["+inode+"]", filepath.Join(fdDir, fmt.Sprint(i))); err != nil {
				t.Fatal(err)
			}
		}
		if cl, ok := cmdlines[pid]; ok {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprint(pid), "cmdline"), []byte(cl), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func withProcRoot(t *testing.T, root string) {
	t.Helper()
	orig := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = orig })
}

func TestFindLocatesAnIPv4Listener(t *testing.T) {
	// 8420 == 0x20E4
	root := fakeProc(t,
		tcpTable([3]string{"00000000:20E4", "0A", "551122"}),
		"",
		map[int][]string{4242: {"551122"}},
		map[int]string{4242: "go\x00run\x00.\x00"},
	)
	withProcRoot(t, root)

	got, err := Find(8420)
	if err != nil {
		t.Fatalf("Find(): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Find() = %#v, want one listener", got)
	}
	if got[0].PID != 4242 || got[0].Port != 8420 || got[0].Stack != "ipv4" {
		t.Errorf("listener = %+v", got[0])
	}
	if got[0].Command != "go run ." {
		t.Errorf("command = %q, want %q", got[0].Command, "go run .")
	}
}

// The blind spot that made a live Vite server read as "not serving": bound to
// ::1 only, so it appears in tcp6 and nowhere else.
func TestFindLocatesAnIPv6OnlyListener(t *testing.T) {
	// 5173 == 0x1435
	root := fakeProc(t,
		tcpTable(),
		tcpTable([3]string{"00000000000000000000000001000000:1435", "0A", "998877"}),
		map[int][]string{777: {"998877"}},
		map[int]string{777: "node\x00vite\x00"},
	)
	withProcRoot(t, root)

	got, err := Find(5173)
	if err != nil {
		t.Fatalf("Find(): %v", err)
	}
	if len(got) != 1 || got[0].PID != 777 {
		t.Fatalf("Find() = %#v, want the IPv6 listener", got)
	}
	if got[0].Stack != "ipv6" {
		t.Errorf("stack = %q, want ipv6", got[0].Stack)
	}
}

// Only LISTEN sockets count. An outbound connection to a port must never be
// mistaken for something holding it, or freeing a port would kill the client.
func TestFindIgnoresNonListeningSockets(t *testing.T) {
	root := fakeProc(t,
		tcpTable([3]string{"00000000:20E4", "01", "551122"}),
		"",
		map[int][]string{4242: {"551122"}},
		nil,
	)
	withProcRoot(t, root)

	got, err := Find(8420)
	if err != nil {
		t.Fatalf("Find(): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Find() = %#v, want nothing — the socket is ESTABLISHED, not LISTEN", got)
	}
}

func TestFindReturnsNothingForAFreePort(t *testing.T) {
	root := fakeProc(t, tcpTable(), tcpTable(), nil, nil)
	withProcRoot(t, root)

	got, err := Find(9999)
	if err != nil {
		t.Fatalf("Find(): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Find() = %#v, want nothing", got)
	}
}

// A different port's listener must not be returned: the hex parse is the only
// thing separating 8420 from 8421.
func TestFindMatchesTheExactPort(t *testing.T) {
	root := fakeProc(t,
		tcpTable(
			[3]string{"00000000:20E4", "0A", "111"}, // 8420
			[3]string{"00000000:20E5", "0A", "222"}, // 8421
		),
		"",
		map[int][]string{10: {"111"}, 20: {"222"}},
		nil,
	)
	withProcRoot(t, root)

	got, err := Find(8421)
	if err != nil {
		t.Fatalf("Find(): %v", err)
	}
	if len(got) != 1 || got[0].PID != 20 {
		t.Fatalf("Find() = %#v, want only pid 20", got)
	}
}

// A missing tcp6 table (IPv6-disabled kernel) is not an error.
func TestFindToleratesAMissingIPv6Table(t *testing.T) {
	root := fakeProc(t, tcpTable([3]string{"00000000:20E4", "0A", "551122"}), "",
		map[int][]string{4242: {"551122"}}, nil)
	withProcRoot(t, root)

	got, err := Find(8420)
	if err != nil {
		t.Fatalf("Find() = %v, want no error when tcp6 is absent", err)
	}
	if len(got) != 1 {
		t.Fatalf("Find() = %#v", got)
	}
}

func TestCommandNameFallsBackToComm(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "55"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "55", "comm"), []byte("dotnet\n"), 0644); err != nil {
		t.Fatal(err)
	}
	withProcRoot(t, root)

	if got := commandName(55); got != "dotnet" {
		t.Fatalf("commandName() = %q, want dotnet", got)
	}
}

func TestCommandNameOfAnUnknownProcess(t *testing.T) {
	withProcRoot(t, t.TempDir())
	if got := commandName(4242); got != "unknown" {
		t.Fatalf("commandName() = %q, want unknown", got)
	}
}

// Kill runs against a real process: SIGTERM first, and the grace callback fires
// so a dev server gets the chance to take its children with it.
func TestKillTerminatesARealProcess(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	graced := false
	if err := Kill(Listener{PID: pid, Port: 1234}, func() { graced = true; time.Sleep(200 * time.Millisecond) }); err != nil {
		t.Fatalf("Kill(): %v", err)
	}
	if !graced {
		t.Error("grace callback never ran — SIGKILL would follow SIGTERM immediately")
	}
	_, _ = cmd.Process.Wait()
	if syscall.Kill(pid, 0) == nil {
		t.Errorf("pid %d survived Kill()", pid)
	}
}

// Killing something already gone is a no-op, not an error: between Find and
// Kill the process may exit on its own.
func TestKillOnAnAlreadyDeadProcessSucceeds(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	if err := Kill(Listener{PID: cmd.Process.Pid}, func() {}); err != nil {
		t.Fatalf("Kill() = %v, want nil for a dead process", err)
	}
}

func TestKillRejectsAnInvalidPID(t *testing.T) {
	if err := Kill(Listener{PID: 0}, func() {}); err == nil {
		t.Fatal("Kill() = nil, want an error for pid 0")
	}
}

// The end-to-end path against a real socket, with the real /proc: bind a port,
// find who holds it, and confirm it is this test process.
func TestFindAgainstARealSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a loopback port: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	got, err := Find(port)
	if err != nil {
		t.Fatalf("Find(): %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("Find(%d) found nothing for a port this process is listening on", port)
	}
	if got[0].PID != os.Getpid() {
		t.Errorf("pid = %d, want this test process %d", got[0].PID, os.Getpid())
	}
}
