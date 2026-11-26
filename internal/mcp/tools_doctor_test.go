package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/doctor"
	"github.com/socialviolation/devstack/internal/ports"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

func fakeDoctorHost(ws *workspace.Workspace) doctor.Host {
	var errored tilt.UIResource
	errored.Metadata.Name = "navexa:api:agent-mine"
	errored.Status.RuntimeStatus = "error"
	return doctor.Host{
		Now:        time.Now,
		DaemonUp:   func() bool { return true },
		DaemonArgs: func() []string { return nil },
		View:       func() (*tilt.TiltView, error) { return &tilt.TiltView{UiResources: []tilt.UIResource{errored}}, nil },
		Listeners:  func([]int) ([]ports.Listener, error) { return nil, nil },
		Owned:      func(int) bool { return true },
		Remote:     func(string, string) (doctor.RemoteState, error) { return doctor.RemoteState{}, nil },
		Repos:      func() []string { return nil },
		Worktrees:  func(string) ([]doctor.Worktree, error) { return nil, nil },
		Dirty:      func(string) (bool, error) { return false, nil },
		Replica:    func() (bool, []doctor.Drift) { return false, nil },
	}
}

func TestDoctorToolGivesAnAgentToolCalls(t *testing.T) {
	ws := guardStore(t, stack.Record{Name: "agent-mine", Base: "navexa", Owner: "alice", Root: t.TempDir(), Active: true, Overlay: []string{"api"}})
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerDoctorTool(s, ws, fakeDoctorHost)

	out := callAs(t, s, alice, "doctor", `{"stack":"agent-mine"}`)
	if !strings.Contains(out, `fix: restart(service=\"api\", stack=\"agent-mine\")`) {
		t.Errorf("agent doctor: %s", out)
	}
	out = callAs(t, s, alice, "doctor", `{}`)
	if !strings.Contains(out, "fix (the owner runs it in a shell): devstack workspace up") {
		t.Errorf("agent workspace doctor: %s", out)
	}
	out = callAs(t, s, owner, "doctor", `{"stack":"agent-mine"}`)
	if !strings.Contains(out, "fix: devstack service restart api --stack agent-mine") {
		t.Errorf("owner doctor: %s", out)
	}
}

func TestCleanupToolIsADryRunByDefault(t *testing.T) {
	ws := guardStore(t, stack.Record{Name: "old", Base: "navexa", Root: "/nowhere/old"})
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws)...)
	registerCleanupTool(s, ws, fakeDoctorHost)

	out := callAs(t, s, owner, "cleanup", `{}`)
	if !strings.Contains(out, "would remove the record of stack old") || !strings.Contains(out, "dry_run=false") {
		t.Errorf("cleanup: %s", out)
	}
	if recs, _ := stack.LoadStore("navexa"); len(recs) != 1 {
		t.Errorf("a dry run changed the store: %+v", recs)
	}
}
