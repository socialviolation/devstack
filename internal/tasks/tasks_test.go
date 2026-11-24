package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/config"
)

func TestRunPassesTheExitCodeThrough(t *testing.T) {
	res, err := Run(context.Background(), t.TempDir(), config.ServiceTask{Run: "echo failing; exit 7"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit != 7 || res.TimedOut {
		t.Errorf("exit = %d timedOut = %v, want 7 and false", res.Exit, res.TimedOut)
	}
	if res.Tail != "failing\n" || res.Truncated {
		t.Errorf("tail = %q truncated = %v", res.Tail, res.Truncated)
	}
}

func TestRunRunsInTheWorkDirWithTheTaskEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "web"), 0755); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), dir, config.ServiceTask{
		Run:     `echo "$(pwd) $GREETING"`,
		WorkDir: "web",
		Env:     map[string]string{"GREETING": "hi"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "web") + " hi\n"
	if res.Exit != 0 || res.Tail != want {
		t.Errorf("exit = %d tail = %q, want 0 and %q", res.Exit, res.Tail, want)
	}
}

func TestRunRefusesAWorkDirOutsideTheWorktree(t *testing.T) {
	for _, wd := range []string{"/tmp", "../elsewhere"} {
		if _, err := Run(context.Background(), t.TempDir(), config.ServiceTask{Run: "true", WorkDir: wd}, 0); err == nil {
			t.Errorf("workDir %q: want a refusal", wd)
		}
	}
}

func TestRunKeepsTheTailOfLongOutput(t *testing.T) {
	res, err := Run(context.Background(), t.TempDir(), config.ServiceTask{Run: "seq 1 1000"}, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Tail) != 16 || !strings.HasSuffix(res.Tail, "999\n1000\n") {
		t.Errorf("tail = %q truncated = %v, want the last 16 bytes", res.Tail, res.Truncated)
	}
}

func TestRunTimeoutKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	start := time.Now()
	res, err := Run(context.Background(), dir, config.ServiceTask{
		Run:     "sleep 30 & echo $! > " + pidFile + "; wait",
		Timeout: "300ms",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Exit != -1 {
		t.Errorf("timedOut = %v exit = %d, want true and -1", res.TimedOut, res.Exit)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Run took %s after a 300ms timeout", took)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the task's child %d outlived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func gone(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(stat))
	return len(fields) > 2 && fields[2] == "Z"
}
