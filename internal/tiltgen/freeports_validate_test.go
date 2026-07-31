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
func TestValidateFreePortsRejectsKillingAnotherServicesPort(t *testing.T) {
	ws := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}),
		svcWithPorts("worker", map[string]int{"http": 8080}, config.FreePortsSpec{}),
	)

	err := ValidateFreePorts([]WorkspaceGen{ws})
	if err == nil {
		t.Fatal("ValidateFreePorts() = nil, want a conflict")
	}
	for _, want := range []string{"navexa:api", "navexa:worker", "8080"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

// Generation must fail, not just warn — a Tiltfile where two supervised
// processes kill each other should never reach the daemon.
func TestGenerateHostFailsOnAFreePortsConflict(t *testing.T) {
	ws := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}),
		svcWithPorts("worker", map[string]int{"http": 8080}, config.FreePortsSpec{}),
	)
	if _, err := GenerateHost([]WorkspaceGen{ws}); err == nil {
		t.Fatal("GenerateHost() = nil, want the conflict to block generation")
	}
}

// Distinct ports are the normal case and must stay silent.
func TestValidateFreePortsAllowsDistinctPorts(t *testing.T) {
	ws := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}),
		svcWithPorts("worker", map[string]int{"http": 9090}, config.FreePortsSpec{All: true}),
	)
	if err := ValidateFreePorts([]WorkspaceGen{ws}); err != nil {
		t.Fatalf("ValidateFreePorts() = %v, want nil", err)
	}
}

// A service freeing its own port is the entire point of the feature.
func TestValidateFreePortsAllowsAServiceFreeingItsOwnPort(t *testing.T) {
	ws := workspaceGen("navexa",
		svcWithPorts("api", map[string]int{"http": 8080, "grpc": 9090}, config.FreePortsSpec{All: true}),
	)
	if err := ValidateFreePorts([]WorkspaceGen{ws}); err != nil {
		t.Fatalf("ValidateFreePorts() = %v, want nil", err)
	}
}

// A stack overlay runs the same service on its own allocated port, so base and
// the stack coexist and neither may free the other's.
func TestValidateFreePortsAllowsBaseAndItsStackOverlay(t *testing.T) {
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

	if err := ValidateFreePorts([]WorkspaceGen{base}); err != nil {
		t.Fatalf("ValidateFreePorts() = %v, want nil — base frees 63290, the stack frees 20005", err)
	}
}

// Two workspaces pinning the same port only becomes dangerous when one of them
// will actually kill: without freePorts it is a bind failure, which is loud and
// harmless. Failing generation there would break setups that work today.
func TestValidateFreePortsIgnoresADuplicatePortNobodyFrees(t *testing.T) {
	a := workspaceGen("alpha", svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{}))
	b := workspaceGen("beta", svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{}))
	if err := ValidateFreePorts([]WorkspaceGen{a, b}); err != nil {
		t.Fatalf("ValidateFreePorts() = %v, want nil", err)
	}
}

// Across workspaces the daemon is still one process supervising both, so the
// flap is identical.
func TestValidateFreePortsCatchesCrossWorkspaceConflicts(t *testing.T) {
	a := workspaceGen("alpha", svcWithPorts("api", map[string]int{"http": 8080}, config.FreePortsSpec{All: true}))
	b := workspaceGen("beta", svcWithPorts("web", map[string]int{"http": 8080}, config.FreePortsSpec{}))

	err := ValidateFreePorts([]WorkspaceGen{a, b})
	if err == nil {
		t.Fatal("ValidateFreePorts() = nil, want a cross-workspace conflict")
	}
	if !strings.Contains(err.Error(), "alpha:api") || !strings.Contains(err.Error(), "beta:web") {
		t.Errorf("error should name both resources:\n%v", err)
	}
}
