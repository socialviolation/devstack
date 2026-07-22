package hostdaemon

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

// Regenerate renders the one host Tiltfile composing every active workspace's
// base services plus each active stack's overlay services, all as distinct
// resources prefixed <ws>:<svc>, and writes it to the host Tilt dir. A running
// host daemon hot-reloads it. With no active workspaces it still writes a valid
// header-only Tiltfile so a running daemon drains to empty. Returns the path.
func Regenerate() (string, error) {
	active, err := workspace.ActiveWorkspaces()
	if err != nil {
		return "", err
	}

	var gens []tiltgen.WorkspaceGen
	for i := range active {
		ws := active[i]
		rw, err := config.ResolveWorkspace(ws.Path)
		if err != nil {
			return "", fmt.Errorf("workspace %q: failed to resolve manifests: %w", ws.Name, err)
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		stackGens, err := ActiveStackGens(&ws)
		if err != nil {
			return "", err
		}
		gens = append(gens, tiltgen.WorkspaceGen{
			Name:     ws.Name,
			Base:     rw,
			BaseOpts: tiltgen.Options{ManagedEnv: workspace.ManagedEnv(&ws, names)},
			Stacks:   stackGens,
		})
	}

	out, err := tiltgen.GenerateHost(gens)
	if err != nil {
		return "", err
	}

	dir := workspace.HostTiltDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create host tilt dir: %w", err)
	}
	path := filepath.Join(dir, "Tiltfile")
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return "", fmt.Errorf("failed to write host Tiltfile: %w", err)
	}
	return path, nil
}

// ActiveStackGens builds a tiltgen.StackGen for every active feature stack of the
// base workspace: its resolved worktree checkout, its overlay-first options, and
// its short name as the namespace. Returns nil when no stack is active.
func ActiveStackGens(ws *workspace.Workspace) ([]tiltgen.StackGen, error) {
	recs, err := stack.LoadStore(ws.Name)
	if err != nil {
		return nil, err
	}
	var gens []tiltgen.StackGen
	for i := range recs {
		rec := recs[i]
		if !rec.Active {
			continue
		}
		rw, err := config.ResolveWorkspace(rec.Root)
		if err != nil {
			return nil, fmt.Errorf("stack %q: failed to resolve manifests: %w", rec.Name, err)
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		opts, err := stack.GenerateOptions(&rec, names)
		if err != nil {
			return nil, fmt.Errorf("stack %q: %w", rec.Name, err)
		}
		gens = append(gens, tiltgen.StackGen{Workspace: rw, Options: opts, Namespace: rec.Name})
	}
	return gens, nil
}

// EnsureDaemon starts the one host Tilt daemon if it is not already running and
// returns a human-readable status line describing what it did. If the daemon is
// reachable or its PID is alive it is left as-is. Otherwise it starts `tilt up`
// in the host Tilt dir on the fixed host port, tracks its PID and session, and
// polls until reachable. It never writes to stdout, so it is safe to call from
// the MCP server whose stdout carries the protocol stream.
func EnsureDaemon() (string, error) {
	apiURL := fmt.Sprintf("http://localhost:%d/api/view", workspace.HostTiltPort)
	if TiltReachable(apiURL) {
		return fmt.Sprintf("Host daemon already running on :%d — it will hot-reload the updated Tiltfile.", workspace.HostTiltPort), nil
	}

	pidFile := workspace.HostPIDFile()
	if pidData, err := os.ReadFile(pidFile); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(pidData))); perr == nil && ProcessAlive(pid) {
			return fmt.Sprintf("Host daemon already running (pid %d, port %d).", pid, workspace.HostTiltPort), nil
		}
		os.Remove(pidFile)
	}

	dir := workspace.HostTiltDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create host data directory %s: %w", dir, err)
	}

	logFile := workspace.HostLogFile()
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to open host log file %s: %w", logFile, err)
	}
	defer lf.Close()

	tiltCmd := exec.Command("tilt", "up", "--host", "0.0.0.0", "--port", strconv.Itoa(workspace.HostTiltPort))
	tiltCmd.Dir = dir
	tiltCmd.Stdout = lf
	tiltCmd.Stderr = lf
	tiltCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := tiltCmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start host daemon: %w", err)
	}

	pid := tiltCmd.Process.Pid
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		tiltCmd.Process.Kill()
		return "", fmt.Errorf("failed to write host PID file: %w", err)
	}
	_, _ = workspace.OpenSession(workspace.HostWorkspace(), pid, []int{workspace.HostTiltPort})

	deadline := time.Now().Add(45 * time.Second)
	reached := false
	for time.Now().Before(deadline) {
		if TiltReachable(apiURL) {
			reached = true
			break
		}
		time.Sleep(2 * time.Second)
	}

	if reached {
		return fmt.Sprintf("Host daemon started (pid %d, port %d, logs: %s)", pid, workspace.HostTiltPort, logFile), nil
	}
	return fmt.Sprintf("Host daemon started but not yet reachable — logs: %s", logFile), nil
}

// TiltReachable reports whether the Tilt API is responding at the given URL.
func TiltReachable(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ProcessAlive reports whether a process with the given PID exists.
func ProcessAlive(pid int) bool {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	_, err := os.Stat(statusPath)
	return err == nil
}
