package panel

import "fmt"

type rowKind int

const (
	rowWorkspace rowKind = iota
	rowGroup
	rowService
)

type row struct {
	kind      rowKind
	workspace string
	stack     string
	label     string
	service   Service
	up        bool
	infra     bool
	running   int
	total     int
}

func (r row) key() string {
	return fmt.Sprintf("%d/%s/%s/%s", r.kind, r.workspace, r.stack, r.service.Name+r.label)
}

func (r row) selectable() bool { return r.kind != rowWorkspace }

func (r row) url() string {
	if len(r.service.URLs) == 0 {
		return ""
	}
	return r.service.URLs[0]
}

func buildRows(snap Snapshot, showAll bool) []row {
	var out []row
	if len(snap.Infra) > 0 {
		out = append(out, row{
			kind: rowGroup, label: "machine", up: true, infra: true,
			running: countRunning(snap.Infra), total: len(snap.Infra),
		})
		for _, svc := range snap.Infra {
			out = append(out, row{kind: rowService, service: svc})
		}
	}

	for _, ws := range snap.Workspaces {
		if len(snap.Workspaces) > 1 {
			out = append(out, row{kind: rowWorkspace, workspace: ws.Name, label: ws.Name})
		}

		if len(ws.Infra) > 0 {
			out = append(out, row{
				kind: rowGroup, workspace: ws.Name, label: "containers", up: true, infra: true,
				running: countRunning(ws.Infra), total: len(ws.Infra),
			})
			for _, svc := range ws.Infra {
				out = append(out, row{kind: rowService, workspace: ws.Name, service: svc})
			}
		}

		out = append(out, row{
			kind: rowGroup, workspace: ws.Name, label: "base", up: true,
			running: countRunning(ws.Base), total: len(ws.Base),
		})
		for _, svc := range keepInteresting(ws.Base, showAll) {
			out = append(out, row{kind: rowService, workspace: ws.Name, service: svc})
		}

		for _, st := range ws.Stacks {
			out = append(out, row{
				kind: rowGroup, workspace: ws.Name, stack: st.Name, label: "stack " + st.Name, up: st.Up,
				running: countRunning(st.Services), total: len(st.Services),
			})
			for _, svc := range keepInteresting(st.Services, showAll) {
				out = append(out, row{kind: rowService, workspace: ws.Name, stack: st.Name, service: svc})
			}
		}
	}
	return out
}

func keepWorkspace(snap Snapshot, focus string) Snapshot {
	if focus == "" {
		return snap
	}
	out := snap
	out.Workspaces = nil
	for _, ws := range snap.Workspaces {
		if ws.Name == focus {
			out.Workspaces = append(out.Workspaces, ws)
		}
	}
	return out
}

func keepInteresting(services []Service, showAll bool) []Service {
	if showAll {
		return services
	}
	out := make([]Service, 0, len(services))
	for _, svc := range services {
		switch svc.State {
		case "stopped", "down", "disabled":
			continue
		}
		out = append(out, svc)
	}
	return out
}

func countRunning(services []Service) int {
	n := 0
	for _, svc := range services {
		if svc.Running() {
			n++
		}
	}
	return n
}
