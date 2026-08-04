package cmd

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/socialviolation/devstack/internal/tunnel"
	"github.com/socialviolation/devstack/internal/workspace"
)

// stubSSH installs a fake ssh recording every invocation. A connectivity check
// has to exit cleanly; a forward carries -N and has to outlive the second
// Launch waits before it calls the forward healthy.
func stubSSH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh")
	record := filepath.Join(dir, "args")
	body := "#!/bin/sh\necho \"$@\" >> " + record + "\ncase \"$*\" in *-N*) sleep 5 ;; esac\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tunnel.SetSSHBin(script))
	return record
}

// serveDaemon answers the daemon view on the fixed host port the tunnel
// commands always talk to. A real daemon on this machine holds that port, so
// the test skips unless it runs somewhere the port is free.
func serveDaemon(t *testing.T, view string) {
	t.Helper()
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", workspace.HostTiltPort))
	if err != nil {
		t.Skipf("host daemon port %d is in use: %v", workspace.HostTiltPort, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(view))
	}))
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
}

// servingPort opens a real listener, since push forwards only ports that are
// actually accepting connections.
func servingPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// captureFaint collects what the commands print through the colour writer.
func captureFaint(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevNo := color.Output, color.NoColor
	color.Output, color.NoColor = &buf, true
	t.Cleanup(func() { color.Output, color.NoColor = prevOut, prevNo })
	return &buf
}

// resetTunnelFlags clears the package-level flag state between commands, the
// way a fresh process would start.
func resetTunnelFlags() {
	tunnelUserFlag = ""
	tunnelServicesFlag = nil
	tunnelReclaimFlag = false
	tunnelStacksFlag = false
	tunnelAsBaseFlag = ""
	tunnelOtelFlag = false
}

// The whole loop, with a stub ssh: a forward records what it did, and a bare
// restart afterwards reads that back and re-establishes the same thing rather
// than the flag defaults. Pull is the case the defaults broke — restart used to
// turn it into a push.
func TestForwardRecordsAndRestartRepeatsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	if err := workspace.Save([]workspace.Workspace{{Name: "navexa", Path: root, TiltPort: workspace.HostTiltPort}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	api, frontend := servingPort(t), servingPort(t)
	serveDaemon(t, fmt.Sprintf(`{"uiResources":[
		{"metadata":{"name":"navexa:api"},"status":{"runtimeStatus":"ok","endpointLinks":[{"url":"http://localhost:%d"}]}},
		{"metadata":{"name":"navexa:frontend"},"status":{"runtimeStatus":"ok","endpointLinks":[{"url":"http://localhost:%d"}]}}
	]}`, api, frontend))
	record := stubSSH(t)

	resetTunnelFlags()
	tunnelServicesFlag = []string{"api"}
	if err := runTunnelForward(tunnel.ModePull, []string{"testhost"}); err != nil {
		t.Fatalf("pull: %v", err)
	}

	if ports := tunnel.TrackedPorts("navexa"); len(ports) != 1 || ports[0] != api {
		t.Fatalf("forwarding %v, want just :%d", ports, api)
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("-L %d:localhost:%d", api, api); !strings.Contains(string(args), want) {
		t.Errorf("ssh was not asked for %q: %s", want, args)
	}

	saved, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatal(err)
	}
	if saved.TunnelLast == nil {
		t.Fatal("the pull recorded nothing, so a restart has nothing to repeat")
	}
	if saved.TunnelLast.Mode != "pull" || saved.TunnelLast.Services != "api" {
		t.Errorf("recorded %+v, want pull of api", *saved.TunnelLast)
	}
	if saved.TunnelHost != "testhost" {
		t.Errorf("remote saved as %q, want testhost", saved.TunnelHost)
	}

	// A new process would start with none of this set. Without the record, the
	// restart below would push, and forward both services instead of one.
	resetTunnelFlags()
	out := captureFaint(t)
	if err := runTunnelRestart(restartCommand(t), nil); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "This repeats the last run: pull --services api") {
		t.Errorf("restart did not say what it was repeating: %q", got)
	}
	if ports := tunnel.TrackedPorts("navexa"); len(ports) != 1 || ports[0] != api {
		t.Errorf("after restart forwarding %v, want just :%d", ports, api)
	}
	if got := string(mustRead(t, record)); strings.Contains(got, "-R ") {
		t.Errorf("restart reversed the direction: %s", got)
	}

	for _, port := range tunnel.TrackedPorts("navexa") {
		tunnel.KillPort("navexa", port)
	}
	resetTunnelFlags()
	restartCommand(t)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The feature itself, end to end: the far end binds the port base normally
// serves and lands on the stack's own port here, so nothing over there is
// reconfigured. Getting the two ends the wrong way round would still start a
// forward, pointing at nothing.
func TestPushAsBaseBindsBasePortOnTheFarEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	if err := workspace.Save([]workspace.Workspace{{Name: "navexa", Path: root, TiltPort: workspace.HostTiltPort}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	baseAPI, stackAPI := servingPort(t), servingPort(t)
	serveDaemon(t, fmt.Sprintf(`{"uiResources":[
		{"metadata":{"name":"navexa:api"},"status":{"runtimeStatus":"ok","endpointLinks":[{"url":"http://localhost:%d"}]}},
		{"metadata":{"name":"navexa:api:agent"},"status":{"runtimeStatus":"ok","endpointLinks":[{"url":"http://localhost:%d"}]}}
	]}`, baseAPI, stackAPI))
	record := stubSSH(t)

	dir := workspace.DataDir("navexa")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stacks.json"),
		[]byte(`[{"name":"agent","base":"navexa","overlay":["api"],"worktrees":{},"ports":{}}]`), 0644); err != nil {
		t.Fatal(err)
	}

	resetTunnelFlags()
	tunnelAsBaseFlag = "agent"
	if err := runTunnelForward(tunnel.ModePush, []string{"testhost"}); err != nil {
		t.Fatalf("push --as-base: %v", err)
	}

	args := string(mustRead(t, record))
	if want := fmt.Sprintf("-R %d:localhost:%d", baseAPI, stackAPI); !strings.Contains(args, want) {
		t.Errorf("ssh was not asked for %q: %s", want, args)
	}
	// The PID file is keyed on the local port, so several stacks can map onto
	// the same port at the far end without colliding here.
	if ports := tunnel.TrackedPorts("navexa"); len(ports) != 1 || ports[0] != stackAPI {
		t.Errorf("forwarding %v, want the stack's own :%d", ports, stackAPI)
	}

	saved, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatal(err)
	}
	if saved.TunnelLast == nil || saved.TunnelLast.AsBase != "agent" {
		t.Errorf("recorded %+v, want the mapping onto base", saved.TunnelLast)
	}

	for _, port := range tunnel.TrackedPorts("navexa") {
		tunnel.KillPort("navexa", port)
	}
	resetTunnelFlags()
}

// deadPort returns a port nothing is listening on.
func deadPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// A workspace where most services are stopped used to print a line each before
// the forwards you asked for. The count carries the same information.
func TestPushSummarisesWhatItSkipped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := workspace.Save([]workspace.Workspace{{Name: "navexa", Path: root, TiltPort: workspace.HostTiltPort}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	live, dead1, dead2 := servingPort(t), deadPort(t), deadPort(t)
	serveDaemon(t, fmt.Sprintf(`{"uiResources":[
		{"metadata":{"name":"navexa:api"},"status":{"runtimeStatus":"ok","endpointLinks":[{"url":"http://localhost:%d"}]}},
		{"metadata":{"name":"navexa:importer"},"status":{"runtimeStatus":"none","endpointLinks":[{"url":"http://localhost:%d"}]}},
		{"metadata":{"name":"navexa:worker"},"status":{"runtimeStatus":"none","endpointLinks":[{"url":"http://localhost:%d"}]}}
	]}`, live, dead1, dead2))
	stubSSH(t)

	resetTunnelFlags()
	out := captureFaint(t)
	if err := runTunnelForward(tunnel.ModePush, []string{"testhost"}); err != nil {
		t.Fatalf("push: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "2 not serving, skipped") {
		t.Errorf("no summary of what was skipped: %q", got)
	}
	for _, name := range []string{"importer", "worker"} {
		if strings.Contains(got, name) {
			t.Errorf("still naming every stopped service (%s): %q", name, got)
		}
	}

	// Naming them yourself makes their absence the answer to your question.
	for _, port := range tunnel.TrackedPorts("navexa") {
		tunnel.KillPort("navexa", port)
	}
	resetTunnelFlags()
	tunnelServicesFlag = []string{"importer", "worker"}
	out2 := captureFaint(t)
	if err := runTunnelForward(tunnel.ModePush, []string{"testhost"}); err != nil {
		t.Fatalf("filtered push: %v", err)
	}
	// Discovery sorts by port and the ports come from the OS, so the two names
	// arrive in either order.
	got2 := out2.String()
	if !strings.Contains(got2, "not serving, skipped:") {
		t.Errorf("a named service that is down went unreported: %q", got2)
	}
	for _, name := range []string{"importer", "worker"} {
		if !strings.Contains(got2, name) {
			t.Errorf("%s was named on the command line and went unreported: %q", name, got2)
		}
	}
	resetTunnelFlags()
}
