package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/socialviolation/devstack/internal/config"
)

const DefaultOutputCap = 64 << 10

var shell = "/bin/sh"

type Result struct {
	Exit      int
	Tail      string
	Truncated bool
	TimedOut  bool
	Duration  time.Duration
}

// Run runs task under sh -c in worktreeDir, or in its workDir below it. The
// task's Run and Env must already be resolved. A timeout kills the whole
// process group, so a task that forks a server can not outlive it. Only the
// last outCap bytes of the combined output are kept.
func Run(ctx context.Context, worktreeDir string, task config.ServiceTask, outCap int) (Result, error) {
	timeout, err := task.ResolvedTimeout()
	if err != nil {
		return Result{}, err
	}
	dir := worktreeDir
	if wd := strings.TrimSpace(task.WorkDir); wd != "" {
		if !filepath.IsLocal(wd) {
			return Result{}, fmt.Errorf("workDir %q must be a relative path inside the worktree", task.WorkDir)
		}
		dir = filepath.Join(worktreeDir, wd)
	}
	if outCap <= 0 {
		outCap = DefaultOutputCap
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &tail{max: outCap}
	cmd := exec.CommandContext(ctx, shell, "-c", task.Run)
	cmd.Dir = dir
	cmd.Env = environ(task.Env)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second

	start := time.Now()
	runErr := cmd.Run()
	res := Result{Duration: time.Since(start)}
	res.Tail, res.Truncated = out.result()

	if ctx.Err() == context.DeadlineExceeded {
		res.Exit = -1
		res.TimedOut = true
		return res, nil
	}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		res.Exit = exitErr.ExitCode()
	default:
		return res, runErr
	}
	return res, nil
}

func environ(vars map[string]string) []string {
	out := os.Environ()
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+vars[k])
	}
	return out
}

type tail struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	dropped bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.dropped = true
	}
	return len(p), nil
}

func (t *tail) result() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf), t.dropped
}
