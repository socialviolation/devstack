package hostdaemon

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

// Regenerate renders the one host Tiltfile composing every active workspace's
// base services plus each active stack's overlay services, all as distinct
// resources prefixed <ws>:<svc>, and writes it to the host Tilt dir. A running
// host daemon hot-reloads it. With no active workspaces it still writes a valid
// header-only Tiltfile so a running daemon drains to empty. Returns the path and
// any warnings generation raised.
func Regenerate() (string, []string, error) {
	res, err := Sync()
	return res.Path, res.Warnings, err
}

// SyncResult reports what Sync did. Wrote is false when the rendered Tiltfile
// already matched the one on disk, so a caller can stay quiet in the common case.
type SyncResult struct {
	Path    string
	Wrote   bool
	Changed []string
	// Warnings are the generation problems that did not stop the render, such as
	// a freePorts reclaim devstack dropped because it would kill another resource.
	Warnings []string
}

// Sync renders the host Tiltfile from the manifests and writes it only when it
// differs from the file on disk, reporting which resources changed. Commands
// that act on a running service call this first so a manifest edit takes effect
// without a separate 'devstack workspace generate'.
func Sync() (SyncResult, error) {
	out, warnings, err := render()
	if err != nil {
		return SyncResult{Warnings: warnings}, err
	}

	dir := workspace.HostTiltDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return SyncResult{Warnings: warnings}, fmt.Errorf("can not create the host tilt directory: %w", err)
	}
	path := filepath.Join(dir, "Tiltfile")

	existing, readErr := os.ReadFile(path)
	if readErr == nil && string(existing) == out {
		return SyncResult{Path: path, Warnings: warnings}, nil
	}

	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return SyncResult{Warnings: warnings}, fmt.Errorf("can not write the host Tiltfile: %w", err)
	}
	return SyncResult{Path: path, Wrote: true, Changed: changedResources(string(existing), out), Warnings: warnings}, nil
}

func render() (string, []string, error) {
	active, err := workspace.ActiveWorkspaces()
	if err != nil {
		return "", nil, err
	}

	var warnings []string
	var gens []tiltgen.WorkspaceGen
	for i := range active {
		ws := active[i]
		// One Tiltfile serves every workspace, so a workspace nobody has built a
		// replica for yet runs from its checkout rather than taking the others down
		// with it.
		rw, err := replica.Resolve(&ws)
		if errors.Is(err, replica.ErrNotBuilt) {
			warnings = append(warnings, fmt.Sprintf("workspace %q has no replica, so it runs from your checkout. To build the replica, run devstack workspace up", ws.Name))
			rw, err = config.ResolveWorkspace(ws.Path)
		}
		if err != nil {
			return "", warnings, fmt.Errorf("workspace %q: can not resolve the manifests: %w", ws.Name, err)
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		stackGens, err := ActiveStackGens(&ws)
		if err != nil {
			return "", warnings, err
		}
		gens = append(gens, tiltgen.WorkspaceGen{
			Name:     ws.Name,
			Base:     rw,
			BaseOpts: tiltgen.Options{ManagedEnv: workspace.ManagedEnv(&ws, names, workspace.ActiveEnvNames(rw, ""))},
			Stacks:   stackGens,
		})
	}

	out, genWarnings, err := tiltgen.GenerateHost(gens)
	return out, append(warnings, genWarnings...), err
}

// TiltfileReloadTimeout bounds how long SyncAndReload waits for the running
// daemon to pick up a Tiltfile it just regenerated.
const TiltfileReloadTimeout = 20 * time.Second

// SyncAndReload brings the generated Tiltfile back in line with the manifests
// and waits for a running daemon to load it, so acting on a service applies
// manifest edits made since the last generate. It returns human-readable notes
// (empty when nothing changed) rather than printing, because its MCP caller owns
// stdout. It never fails the caller: a generation error leaves the daemon on its
// last good Tiltfile, which is still worth acting against, and is reported as a
// note instead.
func SyncAndReload(client *tilt.Client) []string {
	since := time.Now()
	res, err := Sync()
	if err != nil {
		return []string{fmt.Sprintf("⚠ devstack can not regenerate the Tiltfile. The daemon still runs the Tiltfile from the last generation, so it does NOT apply the manifest edits: %v", err)}
	}
	if !res.Wrote {
		return nil
	}

	notes := []string{fmt.Sprintf("↻ The manifests changed, so devstack regenerated %s", res.Path)}
	if len(res.Changed) > 0 {
		notes = append(notes, "  affected: "+strings.Join(res.Changed, ", "))
	}

	// A daemon that isn't up has nothing to reload; the caller reports that.
	if client == nil {
		return notes
	}
	if _, err := client.GetView(); err != nil {
		return notes
	}
	if err := client.WaitForTiltfileReload(since, TiltfileReloadTimeout); err != nil {
		notes = append(notes, fmt.Sprintf("  ⚠ %v. The service can still start with the previous configuration", err))
	}
	return notes
}

// changedResources names the resources whose generated block differs between
// two renderings, including ones added or removed.
func changedResources(before, after string) []string {
	old, new := resourceBlocks(before), resourceBlocks(after)
	seen := map[string]bool{}
	var out []string
	for name, block := range new {
		if old[name] != block {
			out = append(out, name)
		}
		seen[name] = true
	}
	for name := range old {
		if !seen[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// resourceBlocks splits a generated Tiltfile into its per-resource blocks. The
// generator emits every service as a "# <resource name>" comment immediately
// followed by its local_resource( call, so that pair delimits each block and the
// file's own header comment is skipped.
func resourceBlocks(content string) map[string]string {
	lines := strings.Split(content, "\n")
	blocks := map[string]string{}
	name := ""
	var body []string
	flush := func() {
		if name != "" {
			blocks[name] = strings.Join(body, "\n")
		}
		name, body = "", nil
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "# ") && i+1 < len(lines) && lines[i+1] == "local_resource(" {
			flush()
			name = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			continue
		}
		if name != "" {
			body = append(body, line)
		}
	}
	flush()
	return blocks
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
		rw, err := stack.ResolveWorktree(&rec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: devstack skipped stack %q in the host generation: %v\n", rec.Name, err)
			continue
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		opts, err := stack.GenerateOptions(&rec, names)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: devstack skipped stack %q in the host generation: %v\n", rec.Name, err)
			continue
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
		return fmt.Sprintf("The host daemon already runs on :%d. It will hot-reload the updated Tiltfile.", workspace.HostTiltPort), nil
	}

	pidFile := workspace.HostPIDFile()
	if pidData, err := os.ReadFile(pidFile); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(pidData))); perr == nil && ProcessAlive(pid) {
			return fmt.Sprintf("The host daemon already runs (pid %d, port %d).", pid, workspace.HostTiltPort), nil
		}
		os.Remove(pidFile)
	}

	dir := workspace.HostTiltDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("can not create the host data directory %s: %w", dir, err)
	}

	logFile := workspace.HostLogFile()
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("can not open the host log file %s: %w", logFile, err)
	}
	defer lf.Close()

	tiltCmd := exec.Command("tilt", "up", "--host", "0.0.0.0", "--port", strconv.Itoa(workspace.HostTiltPort))
	tiltCmd.Dir = dir
	tiltCmd.Stdout = lf
	tiltCmd.Stderr = lf
	tiltCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := tiltCmd.Start(); err != nil {
		return "", fmt.Errorf("can not start the host daemon: %w", err)
	}

	pid := tiltCmd.Process.Pid
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		tiltCmd.Process.Kill()
		return "", fmt.Errorf("can not write the host PID file: %w", err)
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
		return fmt.Sprintf("The host daemon started (pid %d, port %d, logs: %s)", pid, workspace.HostTiltPort, logFile), nil
	}
	return fmt.Sprintf("The host daemon started, but devstack can not reach it yet. Logs: %s", logFile), nil
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
