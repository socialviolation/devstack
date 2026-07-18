package stack

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

type CreateInput struct {
	Base   *workspace.Workspace
	Name   string
	Repos  []string
	Branch string
}

type OverlayMember struct {
	Service string
	Reason  string
}

type WorktreeResult struct {
	Service  string
	Path     string
	Branch   string
	Detached bool
	Dirty    bool
}

type CreateResult struct {
	StackName    string
	StackRoot    string
	BaseName     string
	BasePath     string
	Overlay      []OverlayMember
	Worktrees    []WorktreeResult
	ManifestPath string
	DaemonPort   int
	Ports        map[string]int
	Warnings     []string
}

type RemoveResult struct {
	Name             string
	BaseName         string
	StackRoot        string
	DaemonPID        int
	RemovedWorktrees []string
	PortsReleased    bool
	Deregistered     bool
	RootRemoved      bool
	Warnings         []string
}

type StackInfo struct {
	Name     string
	BaseName string
	Port     int
	Status   string
	Ports    map[string]int
}

func Create(in CreateInput) (*CreateResult, error) {
	base := in.Base
	if base == nil {
		return nil, fmt.Errorf("no base workspace resolved")
	}
	if base.IsStack() {
		return nil, fmt.Errorf("%q is itself a stack; create a stack from a base workspace", base.Name)
	}
	changed := in.Repos
	if len(changed) == 0 {
		return nil, fmt.Errorf("no repos given: name the service(s) this stack changes")
	}

	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base workspace: %w", err)
	}
	topo, err := config.BuildTopology(base.Path)
	if err != nil {
		return nil, err
	}
	for _, s := range changed {
		if _, ok := topo.Services[s]; !ok {
			return nil, fmt.Errorf("unknown service %q in workspace %q; known services: %s",
				s, base.Name, strings.Join(topo.ServiceNames(), ", "))
		}
	}

	overlay, err := config.OverlaySet(topo, changed)
	if err != nil {
		return nil, err
	}
	changedSet := stringSet(changed)

	res := &CreateResult{
		BaseName: base.Name,
		BasePath: base.Path,
		Ports:    map[string]int{},
	}
	for _, s := range overlay {
		reason := "caller"
		if changedSet[s] {
			reason = "changed"
		}
		res.Overlay = append(res.Overlay, OverlayMember{Service: s, Reason: reason})
	}

	parent := filepath.Dir(base.Path)
	stackRoot := filepath.Join(parent, ".devstack-stacks", in.Name)
	if stackRoot == base.Path || strings.HasPrefix(stackRoot, base.Path+string(os.PathSeparator)) {
		return nil, fmt.Errorf("refusing: stack root %s would be nested under base %s (breaks workspace detection)", stackRoot, base.Path)
	}
	res.StackRoot = stackRoot

	worktreePaths := map[string]string{}
	for _, s := range overlay {
		worktreePaths[s] = filepath.Join(stackRoot, s)
	}
	if err := os.MkdirAll(stackRoot, 0755); err != nil {
		return nil, fmt.Errorf("failed to create stack root %s: %w", stackRoot, err)
	}

	branch := in.Branch
	if branch == "" {
		branch = in.Name
	}
	var dirty []string
	for _, s := range overlay {
		repoPath := baseRW.Services[s].RepoPath
		wt, err := worktree.Create(repoPath, worktreePaths[s], branch, changedSet[s])
		if err != nil {
			return nil, fmt.Errorf("worktree for %q: %w", s, err)
		}
		wr := WorktreeResult{Service: s, Path: worktreePaths[s], Dirty: wt.SourceDirty}
		if changedSet[s] {
			wr.Branch = branch
		} else {
			wr.Detached = true
		}
		res.Worktrees = append(res.Worktrees, wr)
		if wt.SourceDirty {
			dirty = append(dirty, s)
		}
	}
	if len(dirty) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("uncommitted changes left in the base checkout — worktrees hold committed HEAD only: %s", strings.Join(dirty, ", ")))
	}

	stackName := base.Name + "--" + in.Name
	res.StackName = stackName

	manifest, err := config.GenerateStackManifest(baseRW, stackName, overlay, func(s string) string { return worktreePaths[s] })
	if err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal stack manifest: %w", err)
	}
	manifestPath := config.WorkspaceManifestPath(stackRoot)
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write stack manifest: %w", err)
	}
	res.ManifestPath = manifestPath

	if err := workspace.Register(workspace.Workspace{Name: stackName, Path: stackRoot, BaseName: base.Name}); err != nil {
		return nil, fmt.Errorf("failed to register stack: %w", err)
	}
	reg, err := workspace.FindByName(stackName)
	if err != nil {
		return nil, err
	}
	res.DaemonPort = reg.TiltPort

	var keys []string
	for _, s := range overlay {
		svc := baseRW.Services[s]
		if svc.Manifest == nil {
			continue
		}
		portKeys := make([]string, 0, len(svc.Manifest.Ports))
		for k := range svc.Manifest.Ports {
			portKeys = append(portKeys, k)
		}
		sort.Strings(portKeys)
		for _, k := range portKeys {
			keys = append(keys, QualifyPortKey(s, k))
		}
	}
	if len(keys) > 0 {
		allocated, err := workspace.AllocatePorts(stackName, keys)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate service ports: %w", err)
		}
		res.Ports = allocated
	}

	if !daemonReachable(base.TiltPort) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("base %q daemon is not reachable on port %d. A stack reuses base's running services — start base first: (cd %s && devstack up)",
			base.Name, base.TiltPort, base.Path))
	}

	return res, nil
}

func Remove(name string, force bool) (*RemoveResult, error) {
	ws, err := resolveStack(name)
	if err != nil {
		return nil, err
	}

	res := &RemoveResult{Name: ws.Name, BaseName: ws.BaseName, StackRoot: ws.Path}

	pid, err := stopStackDaemon(ws)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("could not stop stack daemon: %v", err))
	}
	res.DaemonPID = pid

	if rw, err := config.ResolveWorkspace(ws.Path); err == nil {
		svcNames := make([]string, 0, len(rw.Services))
		for n := range rw.Services {
			svcNames = append(svcNames, n)
		}
		sort.Strings(svcNames)
		for _, n := range svcNames {
			p := rw.Services[n].RepoPath
			if err := worktree.Remove(p, force); err != nil {
				return res, fmt.Errorf("remove worktree %s: %w\n(use --force to discard uncommitted work)", p, err)
			}
			res.RemovedWorktrees = append(res.RemovedWorktrees, p)
		}
	} else {
		res.Warnings = append(res.Warnings, fmt.Sprintf("could not resolve stack manifest to list worktrees: %v", err))
	}

	if err := workspace.ReleasePorts(ws.Name); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("failed to release ports: %v", err))
	} else {
		res.PortsReleased = true
	}

	if _, err := deregister(ws.Name); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("failed to deregister: %v", err))
	} else {
		res.Deregistered = true
	}

	if err := os.RemoveAll(workspace.DataDir(ws.Name)); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("failed to remove data dir: %v", err))
	}
	if err := os.RemoveAll(ws.Path); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("failed to remove stack root: %v", err))
	} else {
		res.RootRemoved = true
	}

	return res, nil
}

func List() ([]StackInfo, error) {
	all, err := workspace.All()
	if err != nil {
		return nil, fmt.Errorf("failed to load workspace registry: %w", err)
	}
	var stacks []StackInfo
	for _, ws := range all {
		if !ws.IsStack() {
			continue
		}
		ports, _ := workspace.LoadPorts(ws.Name)
		stacks = append(stacks, StackInfo{
			Name:     ws.Name,
			BaseName: ws.BaseName,
			Port:     ws.TiltPort,
			Status:   stackStatus(ws),
			Ports:    ports,
		})
	}
	return stacks, nil
}

func QualifyPortKey(service, key string) string {
	return service + "/" + key
}

func splitPortKey(qualified string) (service, key string, ok bool) {
	i := strings.Index(qualified, "/")
	if i <= 0 || i == len(qualified)-1 {
		return "", "", false
	}
	return qualified[:i], qualified[i+1:], true
}

// GenerateOptions builds the tiltgen options for a feature stack: the
// overlay-first merged PortBook (base's pinned ports with the stack's allocated
// ports layered over its overlay services) and the OTEL export env pointed at the
// base's collector, since a stack never runs its own.
func GenerateOptions(ws *workspace.Workspace, names []string) (tiltgen.Options, error) {
	base, err := workspace.FindByName(ws.BaseName)
	if err != nil {
		return tiltgen.Options{}, fmt.Errorf("stack %q base workspace %q not found in registry: %w", ws.Name, ws.BaseName, err)
	}
	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return tiltgen.Options{}, fmt.Errorf("failed to resolve base workspace %q at %s: %w", base.Name, base.Path, err)
	}

	allocated, err := workspace.LoadPorts(ws.Name)
	if err != nil {
		return tiltgen.Options{}, err
	}
	overlay := config.PortBook{}
	for qualified, port := range allocated {
		service, key, ok := splitPortKey(qualified)
		if !ok {
			continue
		}
		if overlay[service] == nil {
			overlay[service] = map[string]int{}
		}
		overlay[service][key] = port
	}

	return tiltgen.Options{
		ManagedEnv: workspace.ManagedEnv(base, names),
		Book:       config.MergeStackBook(config.BuildPortBook(baseRW), overlay),
	}, nil
}

// resolveStack finds a registered stack by its full name (base--feature) or by
// its short feature name when that is unambiguous across registered stacks.
func resolveStack(name string) (*workspace.Workspace, error) {
	all, err := workspace.All()
	if err != nil {
		return nil, err
	}
	var matches []workspace.Workspace
	for _, ws := range all {
		if !ws.IsStack() {
			continue
		}
		if strings.EqualFold(ws.Name, name) || strings.HasSuffix(strings.ToLower(ws.Name), "--"+strings.ToLower(name)) {
			matches = append(matches, ws)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("stack %q not found", name)
	case 1:
		w := matches[0]
		return &w, nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return nil, fmt.Errorf("stack %q is ambiguous; use the full name: %s", name, strings.Join(names, ", "))
	}
}

// stopStackDaemon stops a stack's dev daemon: disable its services, kill the
// process, remove the PID file, and close the session. A stack has no infra or
// collector of its own, so this is the whole teardown for its daemon. Returns the
// stopped PID, or 0 when no daemon was running.
func stopStackDaemon(ws *workspace.Workspace) (int, error) {
	pidFile := workspace.PIDFile(ws.Name)
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID in %s: %w", pidFile, err)
	}

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	if view, err := tiltClient.GetView(); err == nil {
		for _, r := range view.UiResources {
			if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
				continue
			}
			tiltClient.RunCLI("disable", r.Metadata.Name) //nolint:errcheck
		}
	}
	if proc, err := os.FindProcess(pid); err == nil {
		proc.Kill()
	}
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		return pid, fmt.Errorf("failed to remove PID file: %w", err)
	}

	ports := []int{ws.TiltPort}
	if session, err := workspace.LoadSession(ws.Name); err == nil && len(session.ActivePorts) > 0 {
		ports = session.ActivePorts
	}
	residue := workspace.DetectResidue(pid, ports)
	if err := workspace.CloseSession(ws.Name, residue); err != nil {
		return pid, fmt.Errorf("failed to close session: %w", err)
	}
	return pid, nil
}

func stackStatus(ws workspace.Workspace) string {
	if daemonReachable(ws.TiltPort) {
		return "running"
	}
	if data, err := os.ReadFile(workspace.PIDFile(ws.Name)); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && processAlive(pid) {
			return "starting"
		}
	}
	return "stopped"
}

func deregister(name string) (workspace.Workspace, error) {
	workspaces, err := workspace.Load()
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("failed to load workspace registry: %w", err)
	}
	idx := -1
	for i, ws := range workspaces {
		if strings.EqualFold(ws.Name, name) {
			idx = i
			break
		}
	}
	if idx == -1 {
		return workspace.Workspace{}, fmt.Errorf("workspace %q not found", name)
	}
	removed := workspaces[idx]
	workspaces = append(workspaces[:idx], workspaces[idx+1:]...)
	if err := workspace.Save(workspaces); err != nil {
		return workspace.Workspace{}, fmt.Errorf("failed to save workspace registry: %w", err)
	}
	return removed, nil
}

func daemonReachable(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/api/view", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func processAlive(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d/status", pid))
	return err == nil
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, i := range items {
		set[i] = true
	}
	return set
}
