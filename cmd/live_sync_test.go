package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

const liveServiceManifest = `version: 1
service:
  name: api
runtime:
  autoStart: true
  run:
    command: sh ./run.sh
`

const liveStoppedManifest = `version: 1
service:
  name: worker
runtime:
  autoStart: false
  run:
    command: sh ./run.sh
`

// The script names its own pid file after its directory, so the test reads the
// pid of each copy, and reads nothing at all for a copy that never started.
const liveRunScript = `#!/bin/sh
echo $$ > "$HOME/$(basename "$PWD").pid"
while true; do sleep 2; done
`

func waitForPid(t *testing.T, path string, not int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(data))); cerr == nil && pid != not && pid > 0 {
				if _, serr := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); serr == nil {
					return pid
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("no service pid at %s within %s (previous pid %d)", path, timeout, not)
	return 0
}

func TestLiveWorkspaceUpMovesBaseAndRestartsWhatRuns(t *testing.T) {
	home := filepath.Join(os.Getenv("LIVE_HOME"))
	if home == "" {
		t.Skip("set LIVE_HOME to run the live proof")
	}
	t.Setenv("HOME", home)
	withFreeHostPort(t)

	root := filepath.Join(home, "dev", "shop")
	repo := filepath.Join(root, "api")
	writeAt(t, filepath.Join(repo, "devstack.service.yaml"), liveServiceManifest)
	writeAt(t, filepath.Join(repo, "run.sh"), liveRunScript)
	writeAt(t, filepath.Join(repo, "worker", "devstack.service.yaml"), liveStoppedManifest)
	writeAt(t, filepath.Join(repo, "worker", "run.sh"), liveRunScript)
	gitAt(t, "", "init", "-q", "-b", "main", repo)
	gitAt(t, repo, "config", "commit.gpgsign", "false")
	gitAt(t, repo, "add", "-f", ".")
	gitAt(t, repo, "commit", "-q", "-m", "init")

	writeAt(t, filepath.Join(root, "devstack.workspace.yaml"),
		"version: 1\nworkspace:\n  name: shop\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./api\n      - ./api/worker\n")
	ws := &workspace.Workspace{Name: "shop", Path: root}
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	useWorkspaceKey(t, ws.Name)

	pidPath := filepath.Join(home, "api.pid")
	stoppedPidPath := filepath.Join(home, "worker.pid")
	worktree := filepath.Join(replica.Root(ws), "api")

	t.Cleanup(func() {
		if data, err := os.ReadFile(workspace.HostPIDFile()); err == nil {
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(data))); cerr == nil {
				_ = syscallKill(pid)
			}
		}
		for _, p := range []string{pidPath, stoppedPidPath} {
			if data, err := os.ReadFile(p); err == nil {
				if pid, cerr := strconv.Atoi(strings.TrimSpace(string(data))); cerr == nil {
					_ = syscallKill(pid)
				}
			}
		}
	})

	t.Logf("STEP 1: workspace up on a machine with no replica (daemon port %d)", workspace.HostTiltPort)
	if _, err := bringWorkspaceUp(); err != nil {
		t.Fatalf("bringWorkspaceUp() = %v", err)
	}
	pid1 := waitForPid(t, pidPath, 0, 180*time.Second)
	cwd1, err := os.Readlink("/proc/" + strconv.Itoa(pid1) + "/cwd")
	if err != nil {
		t.Fatalf("read the cwd of pid %d: %v", pid1, err)
	}
	sha1 := gitAt(t, cwd1, "rev-parse", "HEAD")
	t.Logf("EVIDENCE 1: pid %d  /proc/%d/cwd = %s  HEAD = %s", pid1, pid1, cwd1, sha1)

	writeAt(t, filepath.Join(repo, "later.txt"), "this landed on the default branch after base was built\n")
	gitAt(t, repo, "add", "-f", ".")
	gitAt(t, repo, "commit", "-q", "-m", "later")
	tip := gitAt(t, repo, "rev-parse", "HEAD")
	t.Logf("STEP 2: the default branch of the source repository moved to %s", tip)

	t.Log("STEP 3: workspace up again")
	if _, err := bringWorkspaceUp(); err != nil {
		t.Fatalf("second bringWorkspaceUp() = %v", err)
	}
	pid2 := waitForPid(t, pidPath, pid1, 240*time.Second)
	cwd2, err := os.Readlink("/proc/" + strconv.Itoa(pid2) + "/cwd")
	if err != nil {
		t.Fatalf("read the cwd of pid %d: %v", pid2, err)
	}
	sha2 := gitAt(t, cwd2, "rev-parse", "HEAD")
	t.Logf("EVIDENCE 2: pid %d  /proc/%d/cwd = %s  HEAD = %s", pid2, pid2, cwd2, sha2)

	if cwd1 != worktree || cwd2 != worktree {
		t.Errorf("the copies do not run from the replica worktree %s: %s then %s", worktree, cwd1, cwd2)
	}
	if sha1 == sha2 {
		t.Errorf("the replica did not move: it is still at %s", sha1)
	}
	if sha2 != tip {
		t.Errorf("the replica is at %s, want the default branch tip %s", sha2, tip)
	}
	if pid1 == pid2 {
		t.Errorf("the copy did not restart: pid %d serves the code from before the sync", pid1)
	}
	if _, err := os.Stat(filepath.Join(cwd2, "later.txt")); err != nil {
		t.Errorf("the running copy does not hold the new file: %v", err)
	}
	if _, err := os.Stat(stoppedPidPath); err == nil {
		t.Error("`workspace up` started the copy that was stopped")
	} else {
		t.Logf("EVIDENCE 3: the stopped copy wrote no pid file at %s, so devstack never started it", stoppedPidPath)
	}
}

func syscallKill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
