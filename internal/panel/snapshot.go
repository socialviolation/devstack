// Package panel builds the live picture of this machine that the panel draws:
// every workspace, its base services, its feature stacks, the code each copy
// runs, and the address that reaches it from the tailnet.
package panel

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/infra"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/urls"
	"github.com/socialviolation/devstack/internal/workspace"
)

type Service struct {
	Name     string
	Group    string
	Resource string
	State    string
	Ports    []int
	URLs     []string
	Env      string
	Dir      string
	Branch   string
	Detail   string
	Infra    bool
}

func (s Service) Running() bool { return s.State == "running" }

type Stack struct {
	Name     string
	Up       bool
	Branch   string
	Env      string
	Note     string
	Services []Service
}

type Workspace struct {
	Name   string
	Path   string
	Infra  []Service
	Base   []Service
	Stacks []Stack
}

type Snapshot struct {
	Workspaces []Workspace
	// Infra is what the machine runs for every workspace: the one host daemon,
	// and the one telemetry collector.
	Infra    []Service
	TakenAt  time.Time
	DaemonUp bool
	Note     string
}

func (s Snapshot) Services() []Service {
	var out []Service
	for _, ws := range s.Workspaces {
		out = append(out, ws.Base...)
		for _, st := range ws.Stacks {
			out = append(out, st.Services...)
		}
	}
	return out
}

func Take(ctx context.Context) Snapshot {
	snap := Snapshot{TakenAt: time.Now()}

	wss, err := workspace.All()
	if err != nil {
		snap.Note = fmt.Sprintf("can not load the workspace registry: %v", err)
		return snap
	}

	view, viewErr := tilt.NewClient("localhost", workspace.HostTiltPort).GetView()
	snap.DaemonUp = viewErr == nil
	if viewErr != nil {
		view = &tilt.TiltView{}
		snap.Note = fmt.Sprintf("the host daemon on :%d does not answer. Run: devstack workspace up", workspace.HostTiltPort)
	}

	links := urls.Discover(ctx)
	if links.Err != nil && snap.Note == "" {
		snap.Note = links.Err.Error()
	}

	snap.Infra = machineInfra(wss, links, snap.DaemonUp)
	for i := range wss {
		snap.Workspaces = append(snap.Workspaces, takeWorkspace(&wss[i], view, links))
	}
	sort.Slice(snap.Workspaces, func(i, j int) bool { return snap.Workspaces[i].Name < snap.Workspaces[j].Name })
	return snap
}

func takeWorkspace(ws *workspace.Workspace, view *tilt.TiltView, links urls.Map) Workspace {
	out := Workspace{Name: ws.Name, Path: ws.Path}

	cfg, _ := config.Load(ws.Path)
	groups := map[string]string{}
	if cfg != nil {
		for group, members := range cfg.Groups {
			for _, m := range members {
				groups[m] = group
			}
		}
	}

	resources := tilt.ResourceMap(view.UiResources, ws.Name, "")
	names := map[string]bool{}
	for name := range resources {
		names[name] = true
	}
	for name := range groups {
		names[name] = true
	}

	rw, _ := config.ResolveWorkspace(ws.Path)
	envs := activeEnvs(rw, names, "")
	dirs := baseDirs(ws, cfg)

	for _, name := range sorted(names) {
		svc := serviceOf(name, ws.Name, "", resources[name], links)
		svc.Group = groups[name]
		svc.Env = envs[name]
		svc.Dir = dirs[name]
		out.Base = append(out.Base, svc)
	}

	out.Stacks = takeStacks(ws, view, links, groups)
	out.Infra = workspaceInfra(ws)
	readBranches(&out)
	return out
}

func baseDirs(ws *workspace.Workspace, cfg *config.WorkspaceConfig) map[string]string {
	if rw, err := replica.Resolve(ws); err == nil {
		dirs := make(map[string]string, len(rw.Services))
		for name, svc := range rw.Services {
			dirs[name] = svc.RepoPath
		}
		return dirs
	}
	if cfg != nil {
		return cfg.ServicePaths
	}
	return map[string]string{}
}

func takeStacks(ws *workspace.Workspace, view *tilt.TiltView, links urls.Map, groups map[string]string) []Stack {
	recs, err := stack.LoadStore(ws.Name)
	if err != nil {
		return nil
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })

	out := make([]Stack, 0, len(recs))
	for _, rec := range recs {
		resources := tilt.ResourceMap(view.UiResources, ws.Name, rec.Name)
		names := map[string]bool{}
		for _, svc := range rec.Overlay {
			names[svc] = true
		}
		for name := range resources {
			names[name] = true
		}

		// The stack's own worktrees are the only source for the directory a
		// stack copy runs from. Base's resolution answers with base's directory
		// and base's branch, which is the copy the reader is trying to tell
		// apart from this one.
		stackRW, err := stack.ResolveWorktree(&rec)
		if err != nil {
			stackRW = nil
		}
		rw := stackRW
		if rw == nil {
			rw, _ = config.ResolveWorkspace(ws.Path)
		}
		envs := activeEnvs(rw, names, rec.Env)

		st := Stack{Name: rec.Name, Up: rec.Active, Branch: rec.Branch, Env: rec.Env, Note: rec.Note}
		for _, name := range sorted(names) {
			svc := serviceOf(name, ws.Name, rec.Name, resources[name], links)
			svc.Group = groups[name]
			svc.Env = envs[name]
			if stackRW != nil {
				if rs, ok := stackRW.Services[name]; ok {
					svc.Dir = rs.RepoPath
				}
			}
			st.Services = append(st.Services, svc)
		}
		out = append(out, st)
	}
	return out
}

// machineInfra reports what every workspace shares. One host daemon runs the
// services of the whole machine, and one collector takes their telemetry, so
// each belongs to the machine and not to a workspace. A row for each, under
// every workspace, says there are several of them.
func machineInfra(wss []workspace.Workspace, links urls.Map, daemonUp bool) []Service {
	daemon := Service{
		Name: "daemon", Infra: true, State: "erroring",
		Ports:  []int{workspace.HostTiltPort},
		Detail: "devstack workspace up",
	}
	if daemonUp {
		daemon.State = "running"
		daemon.Detail = ""
		daemon.URLs = append(addressesFor(workspace.HostTiltPort, links),
			fmt.Sprintf("http://localhost:%d", workspace.HostTiltPort))
	}

	collector := Service{Name: "otel", Infra: true, State: "disabled", Detail: "devstack otel config on"}
	for i := range wss {
		ws := &wss[i]
		plugin := otel.For(ws)
		if plugin != nil && otel.CollectorRunning() && plugin.CompanionRunning(ws) {
			collector.State = "running"
			collector.Detail = fmt.Sprintf("otlp:%d grpc:%d", workspace.OTLPHTTPPort, workspace.OTLPGRPCPort)
			if ui := otel.QueryEndpointFor(ws); ui != "" {
				collector.URLs = append(addressesFor(portOf(ui), links), ui)
			}
			break
		}
		if config.ObservabilityEnabled(ws.Path) {
			collector.State = "stopped"
			collector.Detail = "devstack otel start"
		}
	}

	return []Service{daemon, collector}
}

// workspaceInfra reports what this workspace alone runs: the containers of its
// compose file.
func workspaceInfra(ws *workspace.Workspace) []Service {
	spec, err := infra.ResolveComposeSpec(ws.Path)
	if err != nil || spec == nil {
		return nil
	}
	running, err := infra.RunningServices(spec)
	if err != nil {
		return nil
	}

	out := make([]Service, 0, len(running))
	for _, name := range running {
		out = append(out, Service{Name: name, Infra: true, State: "running"})
	}
	return out
}

func serviceOf(name, wsName, stackName string, r tilt.UIResource, links urls.Map) Service {
	svc := Service{Name: name, Resource: wsName + ":" + name, State: "down"}
	if stackName != "" {
		svc.Resource += ":" + stackName
	}
	if r.Metadata.Name == "" {
		return svc
	}

	svc.State = tilt.ServiceStatus(r)
	svc.Ports = tilt.EndpointPorts(r.Status.EndpointLinks)
	for _, port := range svc.Ports {
		svc.URLs = append(svc.URLs, addressesFor(port, links)...)
	}
	return svc
}

func addressesFor(port int, links urls.Map) []string {
	var out []string
	for _, link := range links.For(port) {
		out = append(out, link.URL)
	}
	return out
}

func portOf(address string) int {
	u, err := url.Parse(address)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0
	}
	return port
}

func readBranches(ws *Workspace) {
	dirs := map[string]string{}
	collect := func(services []Service) {
		for _, svc := range services {
			if svc.Dir != "" {
				dirs[svc.Dir] = svc.Dir
			}
		}
	}
	collect(ws.Base)
	for _, st := range ws.Stacks {
		collect(st.Services)
	}

	labels := branchLabels(dirs)
	apply := func(services []Service) {
		for i := range services {
			services[i].Branch = labels[services[i].Dir]
		}
	}
	apply(ws.Base)
	for i := range ws.Stacks {
		apply(ws.Stacks[i].Services)
	}
}

const branchCacheTTL = 20 * time.Second

var branchCache = struct {
	sync.Mutex
	at     time.Time
	labels map[string]string
}{}

func branchLabels(dirs map[string]string) map[string]string {
	branchCache.Lock()
	defer branchCache.Unlock()

	fresh := time.Since(branchCache.at) < branchCacheTTL
	if fresh {
		missing := false
		for dir := range dirs {
			if _, ok := branchCache.labels[dir]; !ok {
				missing = true
				break
			}
		}
		if !missing {
			return branchCache.labels
		}
	}

	labels := map[string]string{}
	for dir, info := range gitinfo.ReadAll(dirs) {
		labels[dir] = info.Label()
	}
	if fresh {
		for dir, label := range branchCache.labels {
			if _, ok := labels[dir]; !ok {
				labels[dir] = label
			}
		}
	}
	branchCache.at = time.Now()
	branchCache.labels = labels
	return labels
}

func activeEnvs(rw *config.ResolvedWorkspace, names map[string]bool, stackEnv string) map[string]string {
	out := map[string]string{}
	if rw == nil {
		return out
	}
	wsEnv := rw.Manifest.Workspace.Env
	for name := range names {
		svcEnv := ""
		if rs, ok := rw.Services[name]; ok && rs.Manifest != nil {
			svcEnv = rs.Manifest.Service.Env
		}
		if env := config.ActiveEnvName(wsEnv, svcEnv, stackEnv); env != "" {
			out[name] = env
		}
	}
	return out
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
