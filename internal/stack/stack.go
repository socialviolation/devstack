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
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

const stacksDirName = ".devstack-stacks"

type CreateInput struct {
	Base   *workspace.Workspace
	Name   string
	Repos  []string
	Branch string
	// From is the ref every worktree is cut from. Empty resolves each service
	// repo's default branch instead, origin's copy of it when there is one.
	From string
	Note string
}

type OverlayMember struct {
	Service string
	Reason  string
}

// WorktreeResult is where one service of a stack lives. The worktree is cut per
// repository, so Repo and RepoPath name the worktree, and Path names the
// directory of this service in it. A service at the root of its own repository
// has Path equal to RepoPath.
type WorktreeResult struct {
	Service      string
	Path         string
	Repo         string
	RepoPath     string
	Branch       string
	Ref          string
	Detached     bool
	Dirty        bool
	Materialized []string
}

type CreateResult struct {
	StackName    string
	StackRoot    string
	BaseName     string
	BasePath     string
	Groups       []string
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
	Log      []NoteEntry
	Groups   []string
	Created  time.Time
}

func Create(in CreateInput) (*CreateResult, error) {
	base := in.Base
	if base == nil {
		return nil, fmt.Errorf("devstack can not resolve the base workspace")
	}
	if _, err := FindStack(base.Name, in.Name); err == nil {
		return nil, fmt.Errorf("stack %q already exists in workspace %q", in.Name, base.Name)
	}
	changed := in.Repos
	if len(changed) == 0 {
		return nil, fmt.Errorf("no services given: name the services that this stack changes")
	}

	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return nil, fmt.Errorf("can not resolve the base workspace: %w", err)
	}
	topo, err := config.BuildTopology(base.Path)
	if err != nil {
		return nil, err
	}
	changed, groups, err := expandGroups(topo, base.Name, changed)
	if err != nil {
		return nil, err
	}

	overlay, err := config.OverlaySet(topo, changed)
	if err != nil {
		return nil, err
	}
	changedSet := stringSet(changed)

	res := &CreateResult{
		BaseName: base.Name,
		BasePath: base.Path,
		Groups:   groups,
		Ports:    map[string]int{},
	}
	for _, s := range overlay {
		reason := "caller"
		if changedSet[s] {
			reason = "changed"
		}
		res.Overlay = append(res.Overlay, OverlayMember{Service: s, Reason: reason})
	}

	// The workspace name is in the path because two workspaces can share a
	// parent directory. Without it, a stack name taken in one workspace blocks
	// the same name in the other.
	parent := filepath.Dir(base.Path)
	stackRoot := filepath.Join(parent, stacksDirName, base.Name, in.Name)
	if stackRoot == base.Path || strings.HasPrefix(stackRoot, base.Path+string(os.PathSeparator)) {
		return nil, fmt.Errorf("devstack can not use the stack root %s. The root is under base %s, and a nested root breaks workspace detection", stackRoot, base.Path)
	}
	res.StackRoot = stackRoot

	repos, err := worktree.Plan(overlay, func(s string) string { return baseRW.Services[s].RepoPath }, nil)
	if err != nil {
		return nil, err
	}
	if err := requireEmptyRoot(stackRoot); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stackRoot, 0755); err != nil {
		return nil, fmt.Errorf("can not create the stack root %s: %w", stackRoot, err)
	}

	stackName := base.Name + "--" + in.Name
	res.StackName = stackName

	// A failed create must leave nothing behind. The worktrees hold no work yet,
	// so devstack removes them, and the retry starts from a clean root instead
	// of dying on "already exists".
	var built []string
	allocated := false
	unwind := func() {
		for i := len(built) - 1; i >= 0; i-- {
			_ = worktree.Remove(built[i], true)
		}
		if allocated {
			_ = workspace.ReleasePorts(stackName)
		}
		_ = os.Remove(config.WorkspaceManifestPath(stackRoot))
		_ = os.Remove(stackRoot)
	}

	branch := in.Branch
	if branch == "" {
		branch = in.Name
	}
	worktreePaths := map[string]string{}
	var leftBehind []string
	for _, r := range repos {
		repoWorktree := r.Path(stackRoot)
		changedRepo := r.Changed(changedSet)
		ref := in.From
		if ref == "" {
			_, resolved, err := gitinfo.DefaultRef(r.Toplevel)
			if err != nil {
				unwind()
				return nil, fmt.Errorf("repository %s: %w. Name a ref to cut from with --from", r.Dir, err)
			}
			ref = resolved
		}
		wt, err := worktree.Create(r.Toplevel, repoWorktree, branch, ref, changedRepo)
		if err != nil {
			unwind()
			return nil, fmt.Errorf("worktree for the repository %q: %w", r.Dir, err)
		}
		built = append(built, repoWorktree)
		materialized, err := worktree.MaterializeIgnoredConfig(r.Toplevel, repoWorktree)
		if err != nil {
			unwind()
			return nil, fmt.Errorf("can not materialize the local configuration for the repository %q: %w", r.Dir, err)
		}

		for i, s := range r.Services {
			path := r.ServicePath(stackRoot, s)
			worktreePaths[s] = path
			wr := WorktreeResult{
				Service:  s,
				Path:     path,
				Repo:     r.Dir,
				RepoPath: repoWorktree,
				Ref:      gitinfo.ShortRef(ref),
				Dirty:    wt.SourceDirty,
			}
			if changedRepo {
				wr.Branch = branch
			} else {
				wr.Detached = true
			}
			// The copied files belong to the worktree, so they are reported once
			// and not once for each service in it.
			if i == 0 {
				wr.Materialized = materialized
			}
			res.Worktrees = append(res.Worktrees, wr)
		}
		if wt.SourceDirty || wt.SourceOffRef {
			leftBehind = append(leftBehind, fmt.Sprintf("%s (%s)", gitinfo.ShortRef(ref), r.Dir))
		}
	}
	sort.Slice(res.Worktrees, func(i, j int) bool { return res.Worktrees[i].Service < res.Worktrees[j].Service })
	if len(leftBehind) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("devstack cut the worktrees from %s, not from the base checkout. This stack does not have the uncommitted work in the checkout. This stack does not have the commits that the checkout holds beyond that ref.",
			strings.Join(leftBehind, ", ")))
	}
	res.Warnings = append(res.Warnings, sharedRepoWarnings(baseRW, repos, overlay, in.Name)...)

	manifest, err := config.GenerateStackManifest(baseRW, stackName, overlay, func(s string) string { return worktreePaths[s] })
	if err != nil {
		unwind()
		return nil, err
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		unwind()
		return nil, fmt.Errorf("can not encode the stack manifest: %w", err)
	}
	manifestPath := config.WorkspaceManifestPath(stackRoot)
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		unwind()
		return nil, fmt.Errorf("can not write the stack manifest: %w", err)
	}
	res.ManifestPath = manifestPath

	keys := portKeys(baseRW, overlay)
	if len(keys) > 0 {
		ports, err := workspace.AllocatePorts(stackName, keys)
		if err != nil {
			unwind()
			return nil, fmt.Errorf("can not allocate the service ports: %w", err)
		}
		allocated = true
		res.Ports = ports
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
		Groups:    groups,
		Overlay:   overlayNames,
		Worktrees: worktrees,
		Ports:     res.Ports,
		CreatedAt: time.Now(),
	}); err != nil {
		unwind()
		return nil, fmt.Errorf("can not record the stack: %w", err)
	}

	if !daemonReachable(workspace.HostTiltPort) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("devstack can not reach the host daemon on port %d. A stack uses the services that base runs. Start base first: (cd %s && devstack workspace up)",
			workspace.HostTiltPort, base.Path))
	}

	return res, nil
}

// requireEmptyRoot refuses a stack root that already holds files. devstack
// unwinds a failed create, so files here come from a create that this version
// did not clean up. git worktree add would die on "already exists" and say
// nothing about how to get past it.
func requireEmptyRoot(stackRoot string) error {
	entries, err := os.ReadDir(stackRoot)
	if err != nil || len(entries) == 0 {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return fmt.Errorf("the stack root %s already holds %s. An earlier create did not finish.\nRemove the directory, then create the stack again:\n  rm -rf %s\nIf git still records a worktree there, run 'git worktree prune' in the source repository",
		stackRoot, strings.Join(names, ", "), stackRoot)
}

// sharedRepoWarnings names the services that share a repository with the
// overlay but stay on base.
//
// A repository holds one checkout and one branch, so a stack that overlays one
// service of a repository gets the code of every other service in it. devstack
// does NOT put those services in the overlay: the overlay is what the stack
// runs, and it takes a port, a resource and a hook for each member. A service
// nobody named must not start running a second copy. Base keeps serving it, and
// this warning says so, because the code in the worktree makes the opposite
// look true.
func sharedRepoWarnings(baseRW *config.ResolvedWorkspace, repos []worktree.Repo, overlay []string, stackName string) []string {
	inOverlay := stringSet(overlay)
	byToplevel := make(map[string]string, len(repos))
	for _, r := range repos {
		byToplevel[r.Toplevel] = r.Dir
	}

	others := map[string][]string{}
	names := make([]string, 0, len(baseRW.Services))
	for n := range baseRW.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if inOverlay[n] {
			continue
		}
		top, err := worktree.Toplevel(baseRW.Services[n].RepoPath)
		if err != nil {
			continue
		}
		if dir, ok := byToplevel[top]; ok {
			others[dir] = append(others[dir], n)
		}
	}
	if len(others) == 0 {
		return nil
	}

	dirs := make([]string, 0, len(others))
	for d := range others {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, fmt.Sprintf("The repository %s holds more services than this stack overlays: %s. The worktree holds their code. Base runs them, and this stack does not run them. To run them in this stack, run: devstack stack add %s %s",
			d, strings.Join(others[d], ", "), stackName, strings.Join(others[d], " ")))
	}
	return out
}

// stackWorktreeRoots lists the worktree of every service in a stack, once for
// each repository. Two services in one repository share one worktree, so
// removing it twice fails on the second attempt.
func stackWorktreeRoots(rw *config.ResolvedWorkspace) []string {
	names := make([]string, 0, len(rw.Services))
	for n := range rw.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	seen := map[string]bool{}
	var roots []string
	for _, n := range names {
		path := rw.Services[n].RepoPath
		top, err := worktree.Toplevel(path)
		if err != nil {
			top = path
		}
		if seen[top] {
			continue
		}
		seen[top] = true
		roots = append(roots, top)
	}
	return roots
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
		return fmt.Errorf("devstack can not resolve the base workspace")
	}
	rec, err := FindStack(base.Name, name)
	if err != nil {
		return err
	}
	paths, resolveErr := worktreePaths(rec)
	for _, p := range paths {
		dirty, err := worktree.HasUncommittedChanges(p)
		if err != nil || !dirty {
			continue
		}
		return fmt.Errorf("devstack can not remove the worktree %s. The worktree has uncommitted changes.\nTo discard the uncommitted work, use --force", p)
	}
	if resolveErr != nil {
		return fmt.Errorf("devstack can not resolve the stack manifest at %s, so it can not tell if a worktree holds uncommitted work: %v\nTo remove the stack and everything in it, use --force", rec.Root, resolveErr)
	}
	return nil
}

// worktreePaths lists the worktrees of a stack, and reports why the manifest
// gave no answer. The manifest is the accurate source: it holds where each
// service is now. The record is the fallback, because a stack whose manifest
// devstack can not read still holds worktrees, and those worktrees can still
// hold work that nobody committed.
func worktreePaths(rec *Record) ([]string, error) {
	rw, err := config.ResolveWorkspace(rec.Root)
	if err == nil {
		return stackWorktreeRoots(rw), nil
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range rec.Worktrees {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, err
}

func Remove(base *workspace.Workspace, name string, force bool) (*RemoveResult, error) {
	if base == nil {
		return nil, fmt.Errorf("devstack can not resolve the base workspace")
	}
	rec, err := FindStack(base.Name, name)
	if err != nil {
		return nil, err
	}

	res := &RemoveResult{Name: rec.FullName(), BaseName: rec.Base, StackRoot: rec.Root}

	paths, resolveErr := worktreePaths(rec)
	if resolveErr != nil {
		if !force {
			return res, fmt.Errorf("devstack can not resolve the stack manifest at %s, so it can not tell if a worktree holds uncommitted work: %v\nTo remove the stack and everything in it, use --force", rec.Root, resolveErr)
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("devstack can not resolve the stack manifest, so it removes the worktrees the stack record names: %v", resolveErr))
	}
	for _, p := range paths {
		if err := worktree.Remove(p, force); err != nil {
			if resolveErr == nil {
				return res, fmt.Errorf("devstack can not remove the worktree %s: %w\nTo discard the uncommitted work, use --force", p, err)
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("devstack can not remove the worktree %s: %v", p, err))
			continue
		}
		res.RemovedWorktrees = append(res.RemovedWorktrees, p)
	}

	if err := workspace.ReleasePorts(rec.RuntimeKey()); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("devstack can not release the ports: %v", err))
	} else {
		res.PortsReleased = true
	}

	if ok, err := deleteStack(rec.Base, rec.Name); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("devstack can not remove the stack record: %v", err))
	} else if ok {
		res.Deregistered = true
	}

	if err := os.RemoveAll(workspace.DataDir(rec.RuntimeKey())); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("devstack can not remove the data directory: %v", err))
	}
	if err := os.RemoveAll(rec.Root); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("devstack can not remove the stack root: %v", err))
	} else {
		res.RootRemoved = true
	}

	return res, nil
}

func List(workspaceName string) ([]StackInfo, error) {
	recs, err := LoadStore(workspaceName)
	if err != nil {
		return nil, fmt.Errorf("can not load the stacks store: %w", err)
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
			Log:      rec.Log,
			Groups:   rec.Groups,
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
		return tiltgen.Options{}, fmt.Errorf("stack %q: devstack can not find the base workspace %q in the registry: %w", rec.FullName(), rec.Base, err)
	}
	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return tiltgen.Options{}, fmt.Errorf("can not resolve the base workspace %q at %s: %w", base.Name, base.Path, err)
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
		return nil, fmt.Errorf("stack %q: devstack can not find the base workspace %q in the registry: %w", rec.FullName(), rec.Base, err)
	}
	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return nil, fmt.Errorf("can not resolve the base workspace %q at %s: %w", base.Name, base.Path, err)
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

// expandGroups turns a --repos list into the service names it means, replacing
// any group with its members, and reports which groups were named so the stack
// can record what it was cut to cover.
//
// A name that is both a service and a group is read as the service: --repos
// takes the services a stack changes, and a group that happens to share a
// service's name is the coincidence, not the intent.
func expandGroups(topo *config.TopologyGraph, wsName string, names []string) (services, groups []string, err error) {
	seen := map[string]bool{}
	for _, name := range names {
		if _, ok := topo.Services[name]; ok {
			if !seen[name] {
				seen[name] = true
				services = append(services, name)
			}
			continue
		}
		members, ok := topo.Groups[name]
		if !ok {
			return nil, nil, fmt.Errorf("%q is not a service and not a group in workspace %q\nservices: %s\ngroups:   %s",
				name, wsName, strings.Join(topo.ServiceNames(), ", "), strings.Join(topo.GroupNames(), ", "))
		}
		if len(members) == 0 {
			return nil, nil, fmt.Errorf("group %q in workspace %q has no services", name, wsName)
		}
		groups = append(groups, name)
		for _, m := range members {
			if !seen[m] {
				seen[m] = true
				services = append(services, m)
			}
		}
	}
	return services, groups, nil
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, i := range items {
		set[i] = true
	}
	return set
}
