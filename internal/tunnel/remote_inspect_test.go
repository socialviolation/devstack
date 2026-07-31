package tunnel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRemoteBin builds a PATH holding only the named tools, so the remote
// command can be run for a host that has lsof and for a host that does not.
func fakeRemoteBin(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"grep", "head", "tail"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runRemote(t *testing.T, bin, command string) string {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = []string{"PATH=" + bin}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run remote command: %v\n%s", err, out)
	}
	return string(out)
}

const fakeLsof = `#!/bin/sh
echo "COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME"
echo "node    12345 nick   4u  IPv4  99999      0t0  TCP *:8420 (LISTEN)"
`

const fakeSS = `#!/bin/sh
echo "State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process"
echo "LISTEN 0      4096   0.0.0.0:8420   0.0.0.0:*   users:((\"node\",pid=777,fd=20))"
`

// The bug: a host without lsof fell through to ss, whose single grep-filtered
// line was then discarded as if it were a header, so every port read as free.
func TestRemoteInspectSeesTheHolderWithoutLsof(t *testing.T) {
	bin := fakeRemoteBin(t, map[string]string{"ss": fakeSS})

	out := runRemote(t, bin, remoteInspectCommand(8420))
	if !strings.Contains(out, "pid=777") {
		t.Fatalf("ss branch reported nothing holding 8420:\n%q", out)
	}
	if !strings.HasPrefix(out, "8420\t") {
		t.Errorf("output must start with the port and a tab:\n%q", out)
	}
}

// lsof's header must still be stripped, or every port reports its column titles
// as the holder.
func TestRemoteInspectStripsTheLsofHeader(t *testing.T) {
	bin := fakeRemoteBin(t, map[string]string{"lsof": fakeLsof, "ss": fakeSS})

	out := runRemote(t, bin, remoteInspectCommand(8420))
	if strings.Contains(out, "COMMAND") {
		t.Fatalf("lsof header leaked into the report:\n%q", out)
	}
	if !strings.Contains(out, "node    12345") {
		t.Fatalf("lsof branch reported nothing holding 8420:\n%q", out)
	}
}

// A free port must report as free on either host.
func TestRemoteInspectReportsAFreePort(t *testing.T) {
	empty := `#!/bin/sh
exit 1
`
	header := `#!/bin/sh
echo "State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process"
`
	for name, scripts := range map[string]map[string]string{
		"with lsof":    {"lsof": empty, "ss": header},
		"without lsof": {"ss": header},
	} {
		bin := fakeRemoteBin(t, scripts)
		out := strings.TrimSpace(runRemote(t, bin, remoteInspectCommand(8420)))
		if out != "8420" {
			t.Errorf("%s: port 8420 is free but the report says %q", name, out)
		}
	}
}
