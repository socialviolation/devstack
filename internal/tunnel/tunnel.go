// Package tunnel manages SSH port forwards driven by discovered devstack services.
//
// It mirrors the two modes of the original tunnel.sh:
//
//	push (-R): run on the source machine, expose local service ports on a remote host
//	pull (-L): run on the remote machine, pull ports from the source machine back to here
//
// Discovery reuses the Tilt HTTP API (the same source devstack status uses); each
// forward is a detached `ssh -N` child tracked by a per-port PID file under the
// workspace runtime directory.
package tunnel

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// Mode is the direction of a forward: push (-R) or pull (-L).
type Mode string

const (
	ModePush Mode = "push"
	ModePull Mode = "pull"
)

// Service is a discovered service and one of its exposed ports.
type Service struct {
	Name    string
	Service string // bare service name, without any :stack suffix
	Port    int    // the port on this machine
	Runtime string // Tilt runtimeStatus, e.g. "ok", "pending"

	// RemotePort is the port to occupy on the other machine. Zero means the same
	// port at both ends, which is the usual case; a stack pushed onto base's
	// ports sets it so the far end reaches the stack at the address it expects.
	RemotePort int
}

// Far returns the port this forward occupies on the other machine.
func (s Service) Far() int {
	if s.RemotePort != 0 {
		return s.RemotePort
	}
	return s.Port
}

// Mapped reports whether the two ends use different ports.
func (s Service) Mapped() bool { return s.RemotePort != 0 && s.RemotePort != s.Port }

// sshBin is the ssh executable to invoke. Overridable in tests.
var sshBin = "ssh"

// SetSSHBin points forwards at a different ssh binary and returns a function
// restoring the previous one. It exists so tests in other packages can drive a
// stub rather than open a real connection.
func SetSSHBin(path string) func() {
	prev := sshBin
	sshBin = path
	return func() { sshBin = prev }
}

// sshOpts are the shared ssh flags for a resilient, non-interactive tunnel.
var sshOpts = []string{
	"-N",
	"-o", "ExitOnForwardFailure=yes",
	"-o", "ServerAliveInterval=30",
	"-o", "ServerAliveCountMax=3",
	"-o", "ConnectTimeout=10",
	"-o", "BatchMode=yes",
	"-o", "StrictHostKeyChecking=accept-new",
}

// CheckConnectivity verifies key-based, non-interactive SSH access to the remote
// before any tunnels are attempted. Returns nil on success, or an error carrying
// ssh's own message (auth failure, host unreachable, etc.) so callers can offer
// specific guidance. BatchMode ensures it never blocks on a password prompt.
func CheckConnectivity(user, host string) error {
	cmd := exec.Command(sshBin,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=8",
		fmt.Sprintf("%s@%s", user, host),
		"true",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// Discover returns the services and ports to forward from a Tilt view, scoped to
// one workspace. The host daemon's view namespaces resources as "<ws>:<svc>"
// (base) or "<ws>:<svc>:<stack>" (a feature stack's overlay); only resources
// belonging to wsName are considered, and stack resources are included only when
// includeStacks is true. If filter is non-empty, only services whose bare name
// (<svc>) is in the set are returned, so callers keep passing "api,frontend".
// Each distinct port is returned once (the first service that exposes it wins).
func Discover(view *tilt.TiltView, filter map[string]bool, wsName string, includeStacks bool) []Service {
	var out []Service
	seen := make(map[int]bool)
	for _, r := range view.UiResources {
		svc, name, ok := splitNamespaced(r.Metadata.Name, wsName)
		if !ok {
			continue
		}
		isStack := svc != name
		if isStack && !includeStacks {
			continue
		}
		if len(filter) > 0 && !filter[svc] {
			continue
		}
		for _, link := range r.Status.EndpointLinks {
			port := PortFromURL(link.URL)
			if port == 0 || seen[port] {
				continue
			}
			seen[port] = true
			out = append(out, Service{Name: name, Service: svc, Port: port, Runtime: r.Status.RuntimeStatus})
		}
	}
	return out
}

// splitNamespaced parses a host-daemon resource name "<wsName>:<svc>[:<stack>]".
// It returns the bare service name, the workspace-relative name (<svc> for a base
// resource, <svc>:<stack> for a stack overlay), and whether the resource belongs
// to wsName at all.
func splitNamespaced(resourceName, wsName string) (svc, name string, ok bool) {
	prefix := wsName + ":"
	if !strings.HasPrefix(resourceName, prefix) {
		return "", "", false
	}
	name = resourceName[len(prefix):]
	svc = name
	if i := strings.Index(name, ":"); i >= 0 {
		svc = name[:i]
	}
	return svc, name, true
}

// Listening reports whether a TCP port is accepting connections on localhost —
// i.e. the service is actually up and serving traffic, not merely declared.
func Listening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// PartitionServing splits services into those whose port is currently serving
// traffic locally and those that are not. Used to avoid forwarding dead ports.
func PartitionServing(svcs []Service) (serving, idle []Service) {
	for _, s := range svcs {
		if Listening(s.Port) {
			serving = append(serving, s)
		} else {
			idle = append(idle, s)
		}
	}
	return serving, idle
}

// PortFromURL extracts the numeric port from an endpoint URL, or 0 if absent.
func PortFromURL(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return p
	}
	return 0
}

// Dir returns the directory holding tunnel PID files for a workspace.
func Dir(wsName string) string {
	return filepath.Join(workspace.DataDir(wsName), "tunnels")
}

// pidFile returns the PID file path for a forwarded port.
func pidFile(wsName string, port int) string {
	return filepath.Join(Dir(wsName), fmt.Sprintf("%d.pid", port))
}

// Alive reports whether a process with the given PID is running (and not a
// zombie awaiting reaping). Reads /proc so a killed-but-unreaped forward reads
// as down rather than up.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// Format: "pid (comm) state ..." — comm may contain spaces and parens, so
	// the state char is the second field after the final ')'.
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z'
}

// PID returns the tracked PID for a forwarded port, or 0 if none is tracked.
func PID(wsName string, port int) int {
	data, err := os.ReadFile(pidFile(wsName, port))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// IsUp reports whether the tunnel for a port is tracked and its process alive.
func IsUp(wsName string, port int) bool {
	return Alive(PID(wsName, port))
}

// Launch starts a forward for a single port, replacing any existing one first.
// It spawns a detached `ssh` child, writes a PID file, and verifies the process
// survives an initial second (ExitOnForwardFailure makes ssh exit fast on a bind
// clash). Returns the running PID.
// Launch starts one forward. local is the port on this machine, remote the port
// on the other; ssh orders those two differently for -L and -R, so the mode
// decides which comes first. PID files are keyed on the local port, which is
// unique here even when several stacks map onto the same remote port.
func Launch(wsName string, mode Mode, user, host string, local, remote int) (int, error) {
	port := local
	KillPort(wsName, port)

	if remote == 0 {
		remote = local
	}
	flag := "-L"
	// -L listens here and resolves the target on the far end; -R is the reverse,
	// so the near and far ports swap between the two.
	fwd := fmt.Sprintf("%d:localhost:%d", local, remote)
	if mode == ModePush {
		flag = "-R"
		fwd = fmt.Sprintf("%d:localhost:%d", remote, local)
	}

	args := append([]string{}, sshOpts...)
	args = append(args, flag, fwd, fmt.Sprintf("%s@%s", user, host))

	cmd := exec.Command(sshBin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to start ssh: %w", err)
	}
	pid := cmd.Process.Pid
	// Detach: the CLI exits immediately; setsid keeps ssh alive as an orphan.
	_ = cmd.Process.Release()

	if err := os.MkdirAll(Dir(wsName), 0755); err != nil {
		return 0, fmt.Errorf("failed to create tunnel dir: %w", err)
	}
	if err := os.WriteFile(pidFile(wsName, port), []byte(strconv.Itoa(pid)), 0644); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return 0, fmt.Errorf("failed to write PID file: %w", err)
	}

	time.Sleep(time.Second)
	if !Alive(pid) {
		_ = os.Remove(pidFile(wsName, port))
		return 0, fmt.Errorf("ssh forward for port %d exited immediately", port)
	}
	return pid, nil
}

// KillPort tears down the tunnel for a port: the tracked PID (if any) plus any
// stray ssh processes forwarding the same port. Only ssh client processes are
// touched — never the underlying services.
func KillPort(wsName string, port int) {
	pf := pidFile(wsName, port)
	if pid := PID(wsName, port); pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	_ = os.Remove(pf)

	for _, pid := range strayForwards(port) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}

// TrackedPorts returns the ports this workspace has live forwards for, read from
// its PID files. Stopping and reporting work from this rather than from service
// discovery: a forward outlives the thing that created it, so anything not
// currently discoverable — the observability UI, a service since removed —
// would otherwise be left running with no way to reach it.
func TrackedPorts(wsName string) []int {
	files, err := os.ReadDir(Dir(wsName))
	if err != nil {
		return nil
	}
	var ports []int
	for _, f := range files {
		name := strings.TrimSuffix(f.Name(), ".pid")
		if name == f.Name() {
			continue
		}
		port, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

// trackedForwards returns every PID recorded in any workspace's tunnel PID
// files. It reads the data root rather than the registry so that an
// unregistered workspace's forwards still count as owned.
func trackedForwards() map[int]bool {
	owned := map[int]bool{}
	names, err := os.ReadDir(workspace.DataRoot())
	if err != nil {
		return owned
	}
	for _, n := range names {
		if !n.IsDir() {
			continue
		}
		dir := Dir(n.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".pid") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, f.Name()))
			if err != nil {
				continue
			}
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				owned[pid] = true
			}
		}
	}
	return owned
}

// strayForwards scans /proc for ssh processes forwarding the given port that no
// workspace claims. A forward another workspace tracks is never stray: killing
// it would sabotage that stack, so the clash surfaces via ExitOnForwardFailure
// instead.
func strayForwards(port int) []int {
	fwd := fmt.Sprintf("%d:localhost:%d", port, port)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	owned := trackedForwards()
	self := os.Getpid()
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || owned[pid] {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(string(data), "\x00")
		if len(args) == 0 || filepath.Base(args[0]) != "ssh" {
			continue
		}
		for i, a := range args {
			if (a == "-L" || a == "-R") && i+1 < len(args) && args[i+1] == fwd {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

// RemoteHolder is one process holding a port on the far host.
type RemoteHolder struct {
	Port int
	Info string
}

// InspectRemote reports what holds each port on the remote host, so a caller can
// see whose forward it is about to kill. ReclaimRemote cannot tell a stale
// forward of ours from a live one another stack owns; this is how you find out
// before you destroy it rather than after.
func InspectRemote(user, host string, ports []int) ([]RemoteHolder, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	var b strings.Builder
	for _, p := range ports {
		b.WriteString(remoteInspectCommand(p))
	}
	cmd := exec.Command(sshBin,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=8",
		fmt.Sprintf("%s@%s", user, host),
		b.String(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("could not inspect %s: %w", host, err)
	}

	byPort := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		port, info, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		n, cerr := strconv.Atoi(strings.TrimSpace(port))
		if cerr != nil {
			continue
		}
		if info = strings.TrimSpace(info); info != "" {
			byPort[n] = info
		}
	}

	holders := make([]RemoteHolder, 0, len(ports))
	for _, p := range ports {
		holders = append(holders, RemoteHolder{Port: p, Info: byPort[p]})
	}
	return holders, nil
}

// remoteInspectCommand is the shell devstack runs on the far host for one port:
// it prints the port, a tab, and one line naming what holds it, or nothing.
//
// Each tool strips its own header. lsof prints a header, so its branch drops the
// first line; ss prints a header that the grep already removes, so its branch
// drops nothing. Stripping one line from the pair discarded the ss branch's only
// line, and every port then read as free on a host without lsof.
func remoteInspectCommand(port int) string {
	return fmt.Sprintf("printf '%d\\t'; { lsof -nP -iTCP:%d -sTCP:LISTEN 2>/dev/null | tail -n +2; ss -ltnp 2>/dev/null | grep ':%d '; } | head -1; echo; ", port, port, port)
}

// ReclaimRemote frees the given ports on the remote host, killing whatever is
// bound there so a reverse forward can bind. Best-effort, and indiscriminate:
// it cannot tell a stale forward of ours from a live one another stack owns, so
// callers must keep it opt-in.
func ReclaimRemote(user, host string, ports []int) {
	if len(ports) == 0 {
		return
	}
	var b strings.Builder
	for _, p := range ports {
		fmt.Fprintf(&b, "lsof -ti :%d | xargs kill -9 2>/dev/null; ", p)
	}
	cmd := exec.Command(sshBin,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=8",
		fmt.Sprintf("%s@%s", user, host),
		b.String(),
	)
	_ = cmd.Run()
}
