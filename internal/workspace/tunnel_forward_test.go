package workspace

import "testing"

// The direction and the stack mapping only exist as flags on the command line,
// so they are gone the moment it exits unless the registry keeps them.
func TestUpdateTunnelForwardRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Save([]Workspace{{Name: "navexa", TiltPort: 10300}}); err != nil {
		t.Fatal(err)
	}

	want := TunnelForward{Mode: "pull", AsBase: "agent", Otel: true}
	if err := UpdateTunnelForward("navexa", want); err != nil {
		t.Fatalf("UpdateTunnelForward: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TunnelLast == nil {
		t.Fatalf("nothing recorded: %+v", got)
	}
	if *got[0].TunnelLast != want {
		t.Errorf("recorded %+v, want %+v", *got[0].TunnelLast, want)
	}
}

func TestUpdateTunnelForwardUnknownWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := UpdateTunnelForward("nope", TunnelForward{Mode: "push"}); err == nil {
		t.Fatal("expected an error for an unregistered workspace")
	}
}
