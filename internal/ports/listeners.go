package ports

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Listener is a process holding a TCP listening socket on a port.
type Listener struct {
	Port    int
	PID     int
	Command string
	// Stack reports the address family the socket was found on, so a service
	// bound IPv6-only (Vite's default) is distinguishable from an absent one
	// rather than silently reading as "nothing there".
	Stack string
}

// procNetTCP names the TCP socket tables, one per address family, relative to
// procRoot. Both are read: a listener on one is invisible in the other, and a
// process bound only to ::1 does not appear in the IPv4 table at all.
var procNetTCP = map[string]string{
	"ipv4": "net/tcp",
	"ipv6": "net/tcp6",
}

// procRoot is the proc mount, overridden in tests.
var procRoot = "/proc"

// tcpStateListen is the value /proc/net/tcp uses for LISTEN.
const tcpStateListen = "0A"

// Find returns every process listening on the given port, across both address
// families. An empty result means nothing holds the port.
func Find(port int) ([]Listener, error) {
	inodes := map[string]string{}
	for family, rel := range procNetTCP {
		found, err := listenInodes(filepath.Join(procRoot, rel), port)
		if err != nil {
			continue // a kernel without IPv6 has no tcp6 table; not an error
		}
		for _, inode := range found {
			inodes[inode] = family
		}
	}
	if len(inodes) == 0 {
		return nil, nil
	}

	owners, err := inodeOwners(inodes)
	if err != nil {
		return nil, err
	}
	for i := range owners {
		owners[i].Port = port
	}
	return owners, nil
}

// listenInodes returns the socket inodes listening on port in one /proc/net
// table.
func listenInodes(path string, port int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var inodes []string
	scanner := bufio.NewScanner(f)
	scanner.Scan() // header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != tcpStateListen {
			continue
		}
		local := fields[1]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		p, err := strconv.ParseInt(local[i+1:], 16, 32)
		if err != nil || int(p) != port {
			continue
		}
		inodes = append(inodes, fields[9])
	}
	return inodes, scanner.Err()
}

// inodeOwners maps socket inodes back to the processes holding them by scanning
// every process's open file descriptors for a socket:[inode] link.
func inodeOwners(inodes map[string]string) ([]Listener, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", procRoot, err)
	}

	seen := map[int]bool{}
	var out []Listener
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(procRoot, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // process gone, or not ours to read
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			family, ok := inodes[inode]
			if !ok || seen[pid] {
				continue
			}
			seen[pid] = true
			out = append(out, Listener{PID: pid, Command: commandName(pid), Stack: family})
			break
		}
	}
	return out, nil
}

// commandName reads a process's command line for display, falling back to its
// comm name. A bare pid tells you nothing about whether killing it is safe.
func commandName(pid int) string {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err == nil {
		if s := strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " ")); s != "" {
			return truncate(s, 80)
		}
	}
	data, err = os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	return string(r[:n-1]) + "…"
}

// Kill terminates a listener, escalating from SIGTERM to SIGKILL only if it is
// still alive after the grace period. A dev server given no chance to shut down
// leaves its own children orphaned, which is the mess this is meant to prevent.
func Kill(l Listener, grace func()) error {
	if l.PID <= 0 {
		return fmt.Errorf("invalid pid %d", l.PID)
	}
	if err := syscall.Kill(l.PID, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return fmt.Errorf("SIGTERM to %d: %w", l.PID, err)
	}
	grace()
	if syscall.Kill(l.PID, 0) != nil {
		return nil
	}
	if err := syscall.Kill(l.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("SIGKILL to %d: %w", l.PID, err)
	}
	return nil
}
