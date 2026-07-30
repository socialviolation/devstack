package tiltgen

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func manifestWithFreePorts(spec config.FreePortsSpec, prep string) *config.ServiceManifest {
	return &config.ServiceManifest{
		Version: 1,
		Service: config.ServiceManifestService{Name: "api"},
		Runtime: config.ServiceRuntime{
			Run:  config.ServiceRun{Command: "dotnet run"},
			Prep: config.ServicePrep{Command: prep, FreePorts: spec},
		},
		Ports: map[string]int{"http": 63290},
	}
}

// The bug this replaces: a service's prep hardcoded base's port, the literal was
// copied into every stack worktree, and starting a stack killed base. Freeing is
// now derived from the ports the instance resolved to, so a stack can only reach
// its own.
func TestFreePortsUsesTheInstancesOwnPortNotTheManifestLiteral(t *testing.T) {
	m := manifestWithFreePorts(config.FreePortsSpec{All: true}, "")

	baseBook := config.PortBook{"api": {"http": 63290}}
	stackBook := config.PortBook{"api": {"http": 20000}}

	basePrep, err := prepCommand(m, "api", baseBook)
	if err != nil {
		t.Fatalf("base prepCommand(): %v", err)
	}
	stackPrep, err := prepCommand(m, "api", stackBook)
	if err != nil {
		t.Fatalf("stack prepCommand(): %v", err)
	}

	if !strings.Contains(basePrep, "63290") {
		t.Errorf("base should free its own pinned port, got %q", basePrep)
	}
	if !strings.Contains(stackPrep, "20000") {
		t.Errorf("stack should free its allocated port, got %q", stackPrep)
	}
	if strings.Contains(stackPrep, "63290") {
		t.Fatalf("stack prep reaches base's port — this is the bug: %q", stackPrep)
	}
}

func TestFreePortsRunsBeforeTheServicesOwnPrep(t *testing.T) {
	m := manifestWithFreePorts(config.FreePortsSpec{All: true}, "dotnet build ./api.csproj")
	got, err := prepCommand(m, "api", config.PortBook{"api": {"http": 63290}})
	if err != nil {
		t.Fatalf("prepCommand(): %v", err)
	}
	want := "devstack ports free --quiet 63290 && dotnet build ./api.csproj"
	if got != want {
		t.Fatalf("prep = %q, want %q", got, want)
	}
}

func TestFreePortsNamedKeysOnly(t *testing.T) {
	m := manifestWithFreePorts(config.FreePortsSpec{Keys: []string{"grpc"}}, "")
	m.Ports = map[string]int{"http": 8080, "grpc": 9090}
	got, err := prepCommand(m, "api", config.PortBook{"api": {"http": 8080, "grpc": 9090}})
	if err != nil {
		t.Fatalf("prepCommand(): %v", err)
	}
	if strings.Contains(got, "8080") || !strings.Contains(got, "9090") {
		t.Fatalf("prep = %q, want only the grpc port", got)
	}
}

// A typo in a port key must fail generation. Silently freeing nothing would look
// identical to working right up until a port is held and the service won't bind.
func TestFreePortsRejectsAnUnknownPortKey(t *testing.T) {
	m := manifestWithFreePorts(config.FreePortsSpec{Keys: []string{"htp"}}, "")
	_, err := prepCommand(m, "api", config.PortBook{"api": {"http": 63290}})
	if err == nil || !strings.Contains(err.Error(), `"htp"`) {
		t.Fatalf("prepCommand() = %v, want an error naming the bad key", err)
	}
}

func TestPrepUnchangedWhenFreePortsIsOff(t *testing.T) {
	m := manifestWithFreePorts(config.FreePortsSpec{}, "dotnet build")
	got, err := prepCommand(m, "api", config.PortBook{"api": {"http": 63290}})
	if err != nil {
		t.Fatalf("prepCommand(): %v", err)
	}
	if got != "dotnet build" {
		t.Fatalf("prep = %q, want the service's own prep untouched", got)
	}
}

func TestNoPrepAtAllStaysEmpty(t *testing.T) {
	m := manifestWithFreePorts(config.FreePortsSpec{}, "")
	got, err := prepCommand(m, "api", config.PortBook{"api": {"http": 63290}})
	if err != nil {
		t.Fatalf("prepCommand(): %v", err)
	}
	if got != "" {
		t.Fatalf("prep = %q, want empty", got)
	}
}
