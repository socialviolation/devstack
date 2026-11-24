package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/socialviolation/devstack/internal/ports"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

type Severity int

const (
	Broken Severity = iota
	Degraded
	Tidy
)

var severityTitles = map[Severity]string{Broken: "broken", Degraded: "degraded", Tidy: "tidy-up"}

const (
	KindDaemonDown       = "daemon-down"
	KindDaemonBind       = "daemon-bind"
	KindCopyError        = "copy-error"
	KindCopyMissing      = "copy-missing"
	KindPortClash        = "port-clash"
	KindStaleRecord      = "stale-record"
	KindWorktreeMissing  = "worktree-missing"
	KindBranchGone       = "branch-gone"
	KindBranchBehind     = "branch-behind"
	KindStackExpired     = "stack-expired"
	KindUnpublished      = "unpublished"
	KindOrphanPorts      = "orphan-ports"
	KindOrphanDir        = "orphan-dir"
	KindPrunableWorktree = "prunable-worktree"
	KindReplicaMissing   = "replica-missing"
	KindReplicaDrift     = "replica-drift"
)

type Problem struct {
	Kind     string
	Severity Severity
	Subject  string
	Plain    string
	Fix      string
	Tool     string
}

type RemoteState struct {
	Repo   string
	Gone   bool
	Behind bool
}

type Worktree struct {
	Path     string
	Prunable bool
}

type Host struct {
	Now            func() time.Time
	DaemonUp       func() bool
	DaemonArgs     func() []string
	View           func() (*tilt.TiltView, error)
	Listeners      func([]int) ([]ports.Listener, error)
	Owned          func(pid int) bool
	Remote         func(dir, branch string) (RemoteState, error)
	Repos          func() []string
	Worktrees      func(repo string) ([]Worktree, error)
	Dirty          func(path string) (bool, error)
	Published      func() (map[int]bool, error)
	Replica        func() (bool, []Drift)
	RemoveWorktree func(path string) error
	Prune          func(repo string) error
}

type Report struct {
	Problems []Problem
	Copies   int
	Stacks   int
}

const orphanMinAge = 10 * time.Minute

func Check(ws *workspace.Workspace, stackName string, h Host) (Report, error) {
	var r Report
	recs, err := stack.LoadStore(ws.Name)
	if err != nil {
		return r, err
	}
	scoped := recs
	if stackName != "" {
		scoped = nil
		for _, rec := range recs {
			if strings.EqualFold(rec.Name, stackName) {
				scoped = append(scoped, rec)
			}
		}
		if len(scoped) == 0 {
			return r, fmt.Errorf("stack %q does not exist in workspace %q", stackName, ws.Name)
		}
	}
	r.Stacks = len(scoped)

	up := h.DaemonUp()
	if !up {
		r.Problems = append(r.Problems, Problem{
			Kind: KindDaemonDown, Severity: Broken, Subject: "host daemon",
			Plain: fmt.Sprintf("The host daemon does not answer on :%d, so no service runs.", workspace.HostTiltPort),
			Fix:   "devstack workspace up",
		})
	}
	r.Problems = append(r.Problems, checkBind(h)...)
	if up {
		if view, err := h.View(); err == nil {
			p, copies := checkCopies(ws, stackName, scoped, view, h)
			r.Problems = append(r.Problems, p...)
			r.Copies = copies
		}
	}
	r.Problems = append(r.Problems, checkPorts(scoped, h)...)
	for _, rec := range scoped {
		r.Problems = append(r.Problems, checkRecord(rec, h)...)
	}
	if stackName == "" {
		o := findOrphans(ws, recs, h)
		r.Problems = append(r.Problems, o.problems()...)
		r.Problems = append(r.Problems, checkReplica(h)...)
	}
	sort.SliceStable(r.Problems, func(i, j int) bool { return r.Problems[i].Severity < r.Problems[j].Severity })
	return r, nil
}

func checkBind(h Host) []Problem {
	args := h.DaemonArgs()
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--host" || args[i+1] != "0.0.0.0" {
			continue
		}
		return []Problem{{
			Kind: KindDaemonBind, Severity: Degraded, Subject: "host daemon",
			Plain: "The host daemon still listens on 0.0.0.0, so any machine on the network can reach it.",
			Fix:   "devstack workspace down --all && devstack workspace up",
		}}
	}
	return nil
}

type copyRef struct {
	stack   string
	service string
	res     tilt.UIResource
	found   bool
}

func checkCopies(ws *workspace.Workspace, stackName string, scoped []stack.Record, view *tilt.TiltView, h Host) ([]Problem, int) {
	var all, mine []copyRef
	base := tilt.ResourceMap(view.UiResources, ws.Name, "")
	for _, svc := range sortedKeys(base) {
		c := copyRef{service: svc, res: base[svc], found: true}
		all = append(all, c)
		if stackName == "" {
			mine = append(mine, c)
		}
	}
	for _, rec := range scoped {
		if !rec.Active {
			continue
		}
		res := tilt.ResourceMap(view.UiResources, ws.Name, rec.Name)
		for _, svc := range rec.Overlay {
			r, ok := res[svc]
			c := copyRef{stack: rec.Name, service: svc, res: r, found: ok}
			all = append(all, c)
			mine = append(mine, c)
		}
	}

	var out []Problem
	for _, c := range mine {
		switch {
		case !c.found:
			out = append(out, Problem{
				Kind: KindCopyMissing, Severity: Broken, Subject: copySubject(c),
				Plain: "The stack is up, and the daemon runs no copy of this service.",
				Fix:   "devstack stack up " + c.stack, Tool: fmt.Sprintf("stack_up(name=%q)", c.stack),
			})
		case tilt.ServiceStatus(c.res) == "erroring":
			p := Problem{
				Kind: KindCopyError, Severity: Broken, Subject: copySubject(c),
				Plain: "This copy is in error. Its logs say why.",
				Fix:   fmt.Sprintf("devstack service restart %s --stack %s", c.service, stackOrBase(c.stack)),
			}
			if c.stack != "" {
				p.Tool = fmt.Sprintf("restart(service=%q, stack=%q)", c.service, c.stack)
			}
			out = append(out, p)
		}
	}
	out = append(out, checkPublished(all, mine, h)...)
	return out, len(mine)
}

func checkPublished(all, mine []copyRef, h Host) []Problem {
	if h.Published == nil {
		return nil
	}
	pub, err := h.Published()
	if err != nil || len(pub) == 0 {
		return nil
	}
	publishedSvc := map[string]bool{}
	for _, c := range all {
		for _, p := range tilt.EndpointPorts(c.res.Status.EndpointLinks) {
			if pub[p] {
				publishedSvc[c.service] = true
			}
		}
	}
	var out []Problem
	for _, c := range mine {
		if !c.found || tilt.ServiceStatus(c.res) != "running" || !publishedSvc[c.service] {
			continue
		}
		ps := tilt.EndpointPorts(c.res.Status.EndpointLinks)
		if len(ps) == 0 || anyPublished(ps, pub) {
			continue
		}
		p := Problem{
			Kind: KindUnpublished, Severity: Degraded, Subject: copySubject(c),
			Plain: fmt.Sprintf("This copy runs on :%d, and no tailnet address reaches it. Another copy of %s has one.", ps[0], c.service),
			Fix:   "devstack hooks run workspace.up",
		}
		if c.stack != "" {
			p.Fix = "devstack hooks run stack.up --stack " + c.stack
			p.Tool = fmt.Sprintf("hooks(action=\"run\", event=\"stack.up\", stack=%q)", c.stack)
		}
		out = append(out, p)
	}
	return out
}

func anyPublished(ps []int, pub map[int]bool) bool {
	for _, p := range ps {
		if pub[p] {
			return true
		}
	}
	return false
}

func checkPorts(scoped []stack.Record, h Host) []Problem {
	owner := map[int]string{}
	var want []int
	for _, rec := range scoped {
		ledger, _ := workspace.LoadPorts(rec.RuntimeKey())
		if len(ledger) == 0 {
			ledger = rec.Ports
		}
		for _, key := range sortedKeys(ledger) {
			p := ledger[key]
			if _, ok := owner[p]; ok {
				continue
			}
			owner[p] = fmt.Sprintf("stack %s (%s)", rec.Name, key)
			want = append(want, p)
		}
	}
	if len(want) == 0 {
		return nil
	}
	sort.Ints(want)
	listeners, err := h.Listeners(want)
	if err != nil {
		return nil
	}
	var out []Problem
	for _, l := range listeners {
		if l.PID == 0 || h.Owned(l.PID) {
			continue
		}
		out = append(out, Problem{
			Kind: KindPortClash, Severity: Broken, Subject: fmt.Sprintf("port %d", l.Port),
			Plain: fmt.Sprintf("Port %d belongs to %s, and %s (pid %d), which devstack did not start, holds it.", l.Port, owner[l.Port], l.Command, l.PID),
			Fix:   fmt.Sprintf("devstack ports free %d", l.Port),
		})
	}
	return out
}

func checkRecord(rec stack.Record, h Host) []Problem {
	subject := "stack " + rec.Name
	if !exists(rec.Root) {
		p := Problem{
			Kind: KindStaleRecord, Severity: Tidy, Subject: subject,
			Plain: fmt.Sprintf("The stack's directory %s is gone, and its record stays.", rec.Root),
			Fix:   "devstack cleanup --apply",
		}
		if rec.Active {
			p.Severity = Broken
			p.Fix = fmt.Sprintf("devstack stack down %s && devstack cleanup --apply", rec.Name)
		}
		return []Problem{p}
	}

	var out []Problem
	seenPath, seenRepo := map[string]bool{}, map[string]bool{}
	for _, svc := range sortedKeys(rec.Worktrees) {
		path := rec.Worktrees[svc]
		if path == "" || seenPath[path] {
			continue
		}
		seenPath[path] = true
		if !exists(path) {
			out = append(out, Problem{
				Kind: KindWorktreeMissing, Severity: Broken, Subject: subject + ":" + svc,
				Plain: fmt.Sprintf("The worktree of %s at %s is gone.", svc, path),
				Fix:   fmt.Sprintf("devstack stack rm %s --force", rec.Name), Tool: fmt.Sprintf("stack_rm(name=%q)", rec.Name),
			})
			continue
		}
		st, err := h.Remote(path, rec.Branch)
		if err != nil || st.Repo == "" || seenRepo[st.Repo] {
			continue
		}
		seenRepo[st.Repo] = true
		switch {
		case st.Gone:
			out = append(out, Problem{
				Kind: KindBranchGone, Severity: Tidy, Subject: subject,
				Plain: fmt.Sprintf("Branch %s is gone from origin in %s, so the work likely merged.", rec.Branch, st.Repo),
				Fix:   "devstack stack rm " + rec.Name, Tool: fmt.Sprintf("stack_rm(name=%q)", rec.Name),
			})
		case st.Behind:
			out = append(out, Problem{
				Kind: KindBranchBehind, Severity: Degraded, Subject: subject,
				Plain: fmt.Sprintf("The worktree %s is behind origin/%s.", st.Repo, rec.Branch),
				Fix:   "devstack stack resync " + rec.Name, Tool: fmt.Sprintf("stack_resync(name=%q)", rec.Name),
			})
		}
	}
	if !rec.ExpiresAt.IsZero() && !h.Now().Before(rec.ExpiresAt) {
		out = append(out, Problem{
			Kind: KindStackExpired, Severity: Tidy, Subject: subject,
			Plain: fmt.Sprintf("The stack expired at %s, and gc has not removed it yet.", rec.ExpiresAt.Local().Format(time.RFC3339)),
			Fix:   "devstack stack gc", Tool: fmt.Sprintf("stack_rm(name=%q)", rec.Name),
		})
	}
	return out
}

func checkReplica(h Host) []Problem {
	built, drift := h.Replica()
	if !built {
		return []Problem{{
			Kind: KindReplicaMissing, Severity: Degraded, Subject: "replica",
			Plain: "devstack has built no replica, so base runs from your checkout.",
			Fix:   "devstack workspace up",
		}}
	}
	var out []Problem
	for _, d := range drift {
		out = append(out, Problem{
			Kind: KindReplicaDrift, Severity: Degraded, Subject: "replica:" + d.Name,
			Plain: d.Detail + ".",
			Fix:   "devstack base sync",
		})
	}
	return out
}

type orphanDir struct {
	path      string
	worktrees []repoPath
	dirty     []string
}

type repoPath struct {
	repo string
	path string
}

type orphans struct {
	records  []stack.Record
	ports    []string
	dirs     []orphanDir
	prunable []repoPath
}

func findOrphans(ws *workspace.Workspace, recs []stack.Record, h Host) orphans {
	var o orphans
	now := h.Now()
	names, roots := map[string]bool{}, map[string]bool{}
	for _, rec := range recs {
		names[strings.ToLower(rec.Name)] = true
		roots[filepath.Clean(rec.Root)] = true
		if !exists(rec.Root) && !rec.Active {
			o.records = append(o.records, rec)
		}
	}

	prefix := ws.Name + "--"
	entries, _ := os.ReadDir(workspace.DataRoot())
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || names[strings.ToLower(strings.TrimPrefix(e.Name(), prefix))] {
			continue
		}
		if old(filepath.Join(workspace.DataDir(e.Name()), "ports.json"), now) {
			o.ports = append(o.ports, e.Name())
		}
	}

	stacksRoot := stack.StacksRoot(ws)
	var registered []repoPath
	for _, repo := range h.Repos() {
		wts, err := h.Worktrees(repo)
		if err != nil {
			continue
		}
		for _, wt := range wts {
			if wt.Prunable {
				o.prunable = append(o.prunable, repoPath{repo, wt.Path})
				continue
			}
			registered = append(registered, repoPath{repo, wt.Path})
		}
	}

	dirs, _ := os.ReadDir(stacksRoot)
	for _, d := range dirs {
		path := filepath.Join(stacksRoot, d.Name())
		if !d.IsDir() || roots[path] || !old(path, now) {
			continue
		}
		od := orphanDir{path: path}
		for _, wt := range registered {
			if wt.path != path && !strings.HasPrefix(wt.path, path+string(os.PathSeparator)) {
				continue
			}
			od.worktrees = append(od.worktrees, wt)
			if dirty, err := h.Dirty(wt.path); err != nil || dirty {
				od.dirty = append(od.dirty, wt.path)
			}
		}
		o.dirs = append(o.dirs, od)
	}
	return o
}

func (o orphans) problems() []Problem {
	var out []Problem
	for _, key := range o.ports {
		out = append(out, Problem{
			Kind: KindOrphanPorts, Severity: Tidy, Subject: key,
			Plain: fmt.Sprintf("The port allocation %s belongs to no stack.", workspace.DataDir(key)+"ports.json"),
			Fix:   "devstack cleanup --apply",
		})
	}
	for _, d := range o.dirs {
		p := Problem{
			Kind: KindOrphanDir, Severity: Tidy, Subject: d.path,
			Plain: "This directory belongs to no stack.",
			Fix:   "devstack cleanup --apply",
		}
		if n := len(d.worktrees); n > 0 {
			p.Plain = fmt.Sprintf("This directory belongs to no stack, and it holds %d git worktrees.", n)
		}
		if len(d.dirty) > 0 {
			p.Plain = fmt.Sprintf("This directory belongs to no stack, and the worktree %s holds uncommitted work.", d.dirty[0])
			p.Fix = fmt.Sprintf("git -C %s stash && devstack cleanup --apply", d.dirty[0])
		}
		out = append(out, p)
	}
	byRepo := map[string]int{}
	for _, wt := range o.prunable {
		byRepo[wt.repo]++
	}
	for _, repo := range sortedKeys(byRepo) {
		out = append(out, Problem{
			Kind: KindPrunableWorktree, Severity: Tidy, Subject: repo,
			Plain: fmt.Sprintf("git still lists %d worktrees here whose directory is gone.", byRepo[repo]),
			Fix:   "devstack cleanup --apply",
		})
	}
	return out
}

func (r Report) Render(agent bool) string {
	if len(r.Problems) == 0 {
		return fmt.Sprintf("healthy: %d copies, %d stacks checked\n", r.Copies, r.Stacks)
	}
	var b strings.Builder
	for _, sev := range []Severity{Broken, Degraded, Tidy} {
		var group []Problem
		for _, p := range r.Problems {
			if p.Severity == sev {
				group = append(group, p)
			}
		}
		if len(group) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s (%d)\n", severityTitles[sev], len(group))
		for _, p := range group {
			fmt.Fprintf(&b, "- %s: %s\n", p.Subject, p.Plain)
			switch {
			case !agent:
				fmt.Fprintf(&b, "  fix: %s\n", p.Fix)
			case p.Tool != "":
				fmt.Fprintf(&b, "  fix: %s\n", p.Tool)
			default:
				fmt.Fprintf(&b, "  fix (the owner runs it in a shell): %s\n", p.Fix)
			}
		}
	}
	return b.String()
}

func (r Report) Broken() int {
	n := 0
	for _, p := range r.Problems {
		if p.Severity == Broken {
			n++
		}
	}
	return n
}

func copySubject(c copyRef) string {
	return stackOrBase(c.stack) + ":" + c.service
}

func stackOrBase(name string) string {
	if name == "" {
		return "base"
	}
	return name
}

func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func old(path string, now time.Time) bool {
	info, err := os.Stat(path)
	return err == nil && now.Sub(info.ModTime()) >= orphanMinAge
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
