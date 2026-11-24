package doctor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/ports"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/urls"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

func Live(ws *workspace.Workspace) Host {
	return Host{
		Now: time.Now,
		DaemonUp: func() bool {
			return hostdaemon.TiltReachable(fmt.Sprintf("http://localhost:%d/api/view", workspace.HostTiltPort))
		},
		DaemonArgs: daemonArgs,
		View:       tilt.NewClient("localhost", workspace.HostTiltPort).GetView,
		Listeners:  ports.FindAll,
		Owned:      owned,
		Remote:     remoteState,
		Repos:      func() []string { return baseRepos(ws) },
		Worktrees:  listWorktrees,
		Dirty:      worktree.HasUncommittedChanges,
		Published:  published,
		Replica: func() (bool, []Drift) {
			if !ReplicaBuilt(ws) {
				return false, nil
			}
			return true, ReplicaDrift(ws)
		},
		RemoveWorktree: func(path string) error { return worktree.Remove(path, false) },
		Prune:          worktree.Prune,
	}
}

func daemonPID() int {
	data, err := os.ReadFile(workspace.HostPIDFile())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || !hostdaemon.ProcessAlive(pid) {
		return 0
	}
	return pid
}

func daemonArgs() []string {
	pid := daemonPID()
	if pid == 0 {
		return nil
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}

func owned(pid int) bool {
	daemon := daemonPID()
	for range 64 {
		if pid <= 1 {
			return false
		}
		if pid == daemon {
			return true
		}
		cgroup, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
		if bytes.Contains(cgroup, []byte("/"+tiltgen.SystemdSlice+"/")) || bytes.Contains(cgroup, []byte("/"+hostdaemon.TiltUnit+".service")) {
			return true
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			return false
		}
		fields := strings.Fields(string(stat[i+1:]))
		if len(fields) < 2 {
			return false
		}
		pid, _ = strconv.Atoi(fields[1])
	}
	return false
}

func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func remoteState(dir, branch string) (RemoteState, error) {
	if cur, err := worktree.CurrentBranch(dir); err != nil || cur != branch {
		return RemoteState{}, err
	}
	top, err := worktree.Toplevel(dir)
	if err != nil {
		return RemoteState{}, err
	}
	st := RemoteState{Repo: top}
	out, err := git(top, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return RemoteState{}, err
	}
	if out == "" {
		_, tracked := git(top, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
		st.Gone = tracked == nil
		return st, nil
	}
	remote := strings.Fields(out)[0]
	head, err := worktree.Head(top)
	if err != nil || head == remote {
		return st, err
	}
	if _, err := git(top, "cat-file", "-e", remote+"^{commit}"); err != nil {
		st.Behind = true
		return st, nil
	}
	ahead, err := worktree.IsAncestor(top, remote, head)
	st.Behind = err == nil && !ahead
	return st, nil
}

func baseRepos(ws *workspace.Workspace) []string {
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range resolvedServiceNames(rw.Services) {
		top, err := worktree.Toplevel(rw.Services[name].RepoPath)
		if err != nil || seen[top] {
			continue
		}
		seen[top] = true
		out = append(out, top)
	}
	return out
}

func listWorktrees(repo string) ([]Worktree, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			wts = append(wts, Worktree{Path: strings.TrimPrefix(line, "worktree ")})
		case strings.HasPrefix(line, "prunable") && len(wts) > 0:
			wts[len(wts)-1].Prunable = true
		}
	}
	return wts, nil
}

func published() (map[int]bool, error) {
	m := urls.Discover(context.Background())
	out := map[int]bool{}
	for _, p := range m.Ports() {
		out[p] = true
	}
	if len(out) == 0 && m.Err != nil {
		return nil, m.Err
	}
	return out, nil
}
