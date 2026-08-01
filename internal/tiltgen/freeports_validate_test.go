package tiltgen

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func svcWithPorts(name string, ports map[string]int, free config.FreePortsSpec) config.ResolvedService {
	return config.ResolvedService{
		Name:     name,
		RepoPath: "/repo/" + name,
		Manifest: &config.ServiceManifest{
			Version: 1,
			Service: config.ServiceManifestService{Name: name},
			Runtime: config.ServiceRuntime{
				Run:  config.ServiceRun{Command: "run"},
				Prep: config.ServicePrep{FreePorts: free},
			},
			Ports: ports,
		},
	}
}

func workspaceGen(name string, services ...config.ResolvedService) WorkspaceGen {
	rw := &config.ResolvedWorkspace{
		Manifest: &config.WorkspaceManifest{
			Version:   1,
			Workspace: config.WorkspaceManifestWorkspace{Name: name},
		},
		Services: map[string]config.ResolvedService{},
	}
	for _, s := range services {
		rw.Services[s.Name] = s
	}
	return WorkspaceGen{Name: name, Base: rw}
}

// Two resources the daemon supervises must never be able to kill each other: the
// victim restarts, frees the port back, and they flap indefinitely.
func TestFreePortConflictsFindsKillingAnotherServicesPort(t *testing.T) {
	ws := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}),
		svcWithPorts("worker", map[string]int{"http": 8080}, config.FreePortsSpec{}),
	)

	conflicts, err := FreePortConflicts([]WorkspaceGen{ws})
	if err != nil {
		t.Fatalf("FreePortConflicts(): %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %#v, want one", conflicts)
	}
	for _, want := range []string{"navexa:api", "navexa:worker", "8080"} {
		if !strings.Contains(conflicts[0].Warning(), want) {
			t.Errorf("warning missing %q:\n%s", want, conflicts[0].Warning())
		}
	}
}

// The blast radius that made this worth changing: the host Tiltfile composes
// every registered workspace, so failing generation on one workspace's collision
// removed 'workspace up', 'service stop', 'env set' and 'stack rm' from every
// other workspace on the machine — over a manifest their owners cannot edit.
func TestGenerateHostStillRendersEveryWorkspaceOnAConflict(t *testing.T) {
	bad := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}),
		svcWithPorts("worker", map[string]int{"http": 8080}, config.FreePortsSpec{}),
	)
	good := workspaceGen("tsfc",
		svcWithPorts("web", map[string]int{"http": 4200}, config.FreePortsSpec{All: true}),
	)

	out, warnings, err := GenerateHost([]WorkspaceGen{bad, good})
	if err != nil {
		t.Fatalf("GenerateHost() = %v, want a conflict in one workspace not to block the others", err)
	}
	for _, want := range []string{"navexa:api", "navexa:worker", "tsfc:web"} {
		if !strings.Contains(out, want) {
			t.Errorf("generated Tiltfile is missing resource %q", want)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "navexa:api") || !strings.Contains(warnings[0], "8080") {
		t.Errorf("the warning must name the resource and the port:\n%s", warnings[0])
	}
}

// The reclaim itself is what gets dropped, and only for the resource that
// declared it. An unrelated workspace keeps its own.
func TestGenerateHostDropsOnlyTheConflictingReclaim(t *testing.T) {
	bad := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}),
		svcWithPorts("worker", map[string]int{"http": 8080}, config.FreePortsSpec{}),
	)
	good := workspaceGen("tsfc",
		svcWithPorts("web", map[string]int{"http": 4200}, config.FreePortsSpec{All: true}),
	)

	out, _, err := GenerateHost([]WorkspaceGen{bad, good})
	if err != nil {
		t.Fatalf("GenerateHost(): %v", err)
	}
	if strings.Contains(out, "ports free --quiet 8080") {
		t.Errorf("the conflicting reclaim survived generation:\n%s", out)
	}
	if !strings.Contains(out, "ports free --quiet 4200") {
		t.Errorf("an unrelated workspace lost its own reclaim:\n%s", out)
	}
}

// Distinct ports are the normal case and must stay silent.
func TestFreePortConflictsAllowsDistinctPorts(t *testing.T) {
	ws := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}),
		svcWithPorts("worker", map[string]int{"http": 9090}, config.FreePortsSpec{All: true}),
	)
	conflicts, err := FreePortConflicts([]WorkspaceGen{ws})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("FreePortConflicts() = %#v, %v, want none", conflicts, err)
	}
}

// A service freeing its own port is the entire point of the feature.
func TestFreePortConflictsAllowsAServiceFreeingItsOwnPort(t *testing.T) {
	ws := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080, "grpc": 9090}, config.FreePortsSpec{All: true}),
	)
	conflicts, err := FreePortConflicts([]WorkspaceGen{ws})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("FreePortConflicts() = %#v, %v, want none", conflicts, err)
	}
}

// A stack overlay runs the same service on its own allocated port, so base and
// the stack coexist and neither may free the other's.
func TestFreePortConflictsAllowsBaseAndItsStackOverlay(t *testing.T) {
	base := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 63290}, config.FreePortsSpec{All: true}),
	)
	stackRW := &config.ResolvedWorkspace{
		Manifest: &config.WorkspaceManifest{Version: 1, Workspace: config.WorkspaceManifestWorkspace{Name: "agent"}},
		Services: map[string]config.ResolvedService{
			"api": svcWithPorts("api", map[string]int{"http": 63290}, config.FreePortsSpec{All: true}),
		},
	}
	base.Stacks = []StackGen{{
		Workspace: stackRW,
		Namespace: "agent",
		Options:   Options{Book: config.PortBook{"api": {"http": 20005}}},
	}}

	conflicts, err := FreePortConflicts([]WorkspaceGen{base})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("FreePortConflicts() = %#v, %v — base frees 63290, the stack frees 20005", conflicts, err)
	}
}

// A stack whose allocation collides with base loses its own reclaim. Base's
// other services keep theirs: the drop is per resource, not per workspace.
func TestGenerateHostDropsAStacksReclaimWithoutTouchingBase(t *testing.T) {
	base := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 63290}, config.FreePortsSpec{}),
		svcWithPorts("web", map[string]int{"http": 4200}, config.FreePortsSpec{All: true}),
	)
	stackRW := &config.ResolvedWorkspace{
		Manifest: &config.WorkspaceManifest{Version: 1, Workspace: config.WorkspaceManifestWorkspace{Name: "agent"}},
		Services: map[string]config.ResolvedService{
			"api": svcWithPorts("api", map[string]int{"http": 63290}, config.FreePortsSpec{All: true}),
		},
	}
	base.Stacks = []StackGen{{
		Workspace: stackRW,
		Namespace: "agent",
		Options:   Options{Book: config.PortBook{"api": {"http": 63290}}},
	}}

	out, warnings, err := GenerateHost([]WorkspaceGen{base})
	if err != nil {
		t.Fatalf("GenerateHost(): %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "navexa:api:agent") {
		t.Fatalf("warnings = %#v, want one naming the stack's resource", warnings)
	}
	if strings.Contains(out, "ports free --quiet 63290") {
		t.Errorf("the stack kept a reclaim that would kill base:\n%s", out)
	}
	if !strings.Contains(out, "ports free --quiet 4200") {
		t.Errorf("base's unrelated service lost its own reclaim:\n%s", out)
	}
}

// Two workspaces pinning the same port only becomes dangerous when one of them
// will actually kill: without freePorts it is a bind failure, which is loud and
// harmless. Warning there would flag setups that work today.
func TestFreePortConflictsIgnoresADuplicatePortNobodyFrees(t *testing.T) {
	a := workspaceGen("alpha", svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{}))
	b := workspaceGen("beta", svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{}))
	conflicts, err := FreePortConflicts([]WorkspaceGen{a, b})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("FreePortConflicts() = %#v, %v, want none", conflicts, err)
	}
}

// Across workspaces the daemon is still one process supervising both, so the
// flap is identical.
func TestFreePortConflictsCatchesCrossWorkspaceConflicts(t *testing.T) {
	a := workspaceGen("alpha", svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}))
	b := workspaceGen("beta", svcWithPorts("web", map[string]int{"http": 8080}, config.FreePortsSpec{}))

	conflicts, err := FreePortConflicts([]WorkspaceGen{a, b})
	if err != nil {
		t.Fatalf("FreePortConflicts(): %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %#v, want one", conflicts)
	}
	w := conflicts[0].Warning()
	if !strings.Contains(w, "alpha:api") || !strings.Contains(w, "beta:web") {
		t.Errorf("the warning should name both resources:\n%s", w)
	}
}
