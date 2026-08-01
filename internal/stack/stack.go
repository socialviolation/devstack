package stack

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

type CreateInput struct {
	Base   *workspace.Workspace
	Name   string
	Repos  []string
	Branch string
	Note   string
}

type OverlayMember struct {
	Service string
	Reason  string
}

type WorktreeResult struct {
	Service      string
	Path         string
	Branch       string
	Detached     bool
	Dirty        bool
	Materialized []string
}

type CreateResult struct {
	StackName    string
	StackRoot    string
	BaseName     string
	BasePath     string
	Overlay      []OverlayMember
	Worktrees    []WorktreeResult
	ManifestPath string
	Ports        map[string]int
	Warnings     []string
}

type RemoveResult struct {
	Name             string
	BaseName         string
	StackRoot        string
	RemovedWorktrees []string
	PortsReleased    bool
	Deregistered     bool
	RootRemoved      bool
	Warnings         []string
}

type StackInfo struct {
	Name     string
	BaseName string
	BasePort int
	Status   string
	Ports    map[string]int
	Services []string
	Branch   string
	Env      string
	Note     string
	Created  time.Time
}

func Create(in CreateInput) (*CreateResult, error) {
	base := in.Base
	if base == nil {
		return nil, fmt.Errorf("no base workspace resolved")
	}
	if _, err := FindStack(base.Name, in.Name); err == nil {
		return nil, fmt.Errorf("stack %q already exists in workspace %q", in.Name, base.Name)
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
		materialized, err := worktree.MaterializeIgnoredConfig(repoPath, worktreePaths[s])
		if err != nil {
			return nil, fmt.Errorf("materialize local config for %q: %w", s, err)
		}
		wr.Materialized = materialized
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

	worktrees := map[string]string{}
	for s, p := range worktreePaths {
		worktrees[s] = p
	}
	overlayNames := make([]string, len(overlay))
	copy(overlayNames, overlay)
	if err := upsertStack(Record{
		Name:      in.Name,
		Base:      base.Name,
		Root:      stackRoot,
		Branch:    branch,
		Note:      in.Note,
		Overlay:   overlayNames,
		Worktrees: worktrees,
		Ports:     res.Ports,
		CreatedAt: time.Now(),
	}); err != nil {
		return nil, fmt.Errorf("failed to record stack: %w", err)
	}

	if !daemonReachable(workspace.HostTiltPort) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("host daemon is not reachable on port %d. A stack reuses base's running services — start base first: (cd %s && devstack workspace up)",
			workspace.HostTiltPort, base.Path))
	}

	return res, nil
}

// CheckRemovable reports the refusal Remove will raise, before anything runs.
//
// Remove fires teardown hooks first, and those hooks de-provision state outside
// this machine. A refusal after they run leaves the stack alive and already
// de-provisioned, and a second attempt runs them again. Callers pre-flight with
// this and stop before the hooks.
//
// It probes only the documented refusal: a worktree holding uncommitted work. A
// worktree it cannot read is left to Remove, which reports it in context.
func CheckRemovable(base *workspace.Workspace, name string, force bool) error {
	if force {
		return nil
	}
	if base == nil {
		return fmt.Errorf("no base workspace resolved")
	}
	rec, err := FindStack(base.Name, name)
	if err != nil {
		return err
	}
	rw, err := config.ResolveWorkspace(rec.Root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(rw.Services))
	for n := range rw.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := rw.Services[n].RepoPath
		dirty, err := worktree.HasUncommittedChanges(p)
		if err != nil || !dirty {
			continue
		}
		return fmt.Errorf("remove worktree %s: worktree %s has uncommitted changes; refusing to remove without force\n(use --force to discard uncommitted work)", p, p)
	}
	return nil
}

func Remove(base *workspace.Workspace, name string, force bool) (*RemoveResult, error) {
	if base == nil {
		return nil, fmt.Errorf("no base workspace resolved")
	}
	rec, err := FindStack(base.Name, name)
	if err != nil {
		return nil, err
	}

	res := &RemoveResult{Name: rec.FullName(), BaseName: rec.Base, StackRoot: rec.Root}

	if rw, err := config.ResolveWorkspace(rec.Root); err == nil {
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

	if err := workspace.ReleasePorts(rec.RuntimeKey()); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("failed to release ports: %v", err))
	} else {
		res.PortsReleased = true
	}

	if ok, err := deleteStack(rec.Base, rec.Name); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("failed to remove stack record: %v", err))
	} else if ok {
		res.Deregistered = true
	}

	if err := os.RemoveAll(workspace.DataDir(rec.RuntimeKey())); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("failed to remove data dir: %v", err))
	}
	if err := os.RemoveAll(rec.Root); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("failed to remove stack root: %v", err))
	} else {
		res.RootRemoved = true
	}

	return res, nil
}

func List(workspaceName string) ([]StackInfo, error) {
	recs, err := LoadStore(workspaceName)
	if err != nil {
		return nil, fmt.Errorf("failed to load stacks store: %w", err)
	}
	basePort := workspace.HostTiltPort
	var stacks []StackInfo
	for _, rec := range recs {
		ports, _ := workspace.LoadPorts(rec.RuntimeKey())
		if len(ports) == 0 {
			ports = rec.Ports
		}
		stacks = append(stacks, StackInfo{
			Name:     rec.FullName(),
			BaseName: rec.Base,
			BasePort: basePort,
			Status:   stackStatus(rec),
			Ports:    ports,
			Services: rec.Overlay,
			Branch:   rec.Branch,
			Env:      rec.Env,
			Note:     rec.Note,
			Created:  rec.CreatedAt,
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
func GenerateOptions(rec *Record, names []string) (tiltgen.Options, error) {
	base, err := workspace.FindByName(rec.Base)
	if err != nil {
		return tiltgen.Options{}, fmt.Errorf("stack %q base workspace %q not found in registry: %w", rec.FullName(), rec.Base, err)
	}
	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return tiltgen.Options{}, fmt.Errorf("failed to resolve base workspace %q at %s: %w", base.Name, base.Path, err)
	}

	allocated, err := workspace.LoadPorts(rec.RuntimeKey())
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
		ManagedEnv: workspace.ManagedEnvFor(base, names, rec.Name, workspace.ActiveEnvNames(baseRW, rec.Env)),
		Book:       config.MergeStackBook(config.BuildPortBook(baseRW), overlay),
		StackEnv:   rec.Env,
	}, nil
}

// ResolveWorktree resolves a stack's worktree workspace and folds in the base
// workspace's environment definitions and workspace-scope env selection, since a
// stack inherits — never redefines — base's environments.
func ResolveWorktree(rec *Record) (*config.ResolvedWorkspace, error) {
	base, err := workspace.FindByName(rec.Base)
	if err != nil {
		return nil, fmt.Errorf("stack %q base workspace %q not found in registry: %w", rec.FullName(), rec.Base, err)
	}
	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base workspace %q at %s: %w", base.Name, base.Path, err)
	}
	rw, err := config.ResolveWorkspace(rec.Root)
	if err != nil {
		return nil, err
	}
	inheritBaseEnv(rw.Manifest, baseRW.Manifest)
	return rw, nil
}

func inheritBaseEnv(worktree, base *config.WorkspaceManifest) {
	worktree.Environments = base.Environments
	if worktree.Workspace.Env == "" {
		worktree.Workspace.Env = base.Workspace.Env
	}
}

// Resolve returns a base workspace's stack by short feature name.
func Resolve(workspaceName, name string) (*Record, error) {
	return FindStack(workspaceName, name)
}

// stackStatus reports whether a stack's overlay services are folded into the base
// workspace's Tiltfile. An up stack's resources are present (and run in base's one
// daemon); a down stack's are not. Per-resource running state is read from the base
// daemon by the caller, not here.
//
// The words are "up" and "down" because `devstack stack up` and `devstack stack
// down` are what set them. Calling the same state "active" here while `status`
// called it "idle" and the briefing called it "stopped" left one stack described
// three ways in three places, and no way to tell they meant the same thing.
func stackStatus(rec Record) string {
	if rec.Active {
		return StatusUp
	}
	return StatusDown
}

// StatusUp and StatusDown are the only two states a stack has. They are
// constants because a caller in another package compared against the literal
// "active" and silently took the wrong branch the moment the word changed.
const (
	StatusUp   = "up"
	StatusDown = "down"
)

// DaemonReachable reports whether a dev daemon is serving its API on the given
// port. Callers use it to fail fast with a clear message instead of hanging when
// the daemon isn't running.
func DaemonReachable(port int) bool {
	return daemonReachable(port)
}

var daemonReachable = func(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/api/view", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, i := range items {
		set[i] = true
	}
	return set
}
