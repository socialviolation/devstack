package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/socialviolation/devstack/internal/tunnel"
	"github.com/socialviolation/devstack/internal/workspace"
)

// restartCommand returns the registered restart command with every flag back at
// its default, so one case's flags don't leak into the next.
func restartCommand(t *testing.T) *cobra.Command {
	t.Helper()
	for _, c := range tunnelCmd.Commands() {
		if c.Name() == "restart" {
			c.Flags().VisitAll(func(f *pflag.Flag) {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			})
			return c
		}
	}
	t.Fatal("tunnel restart is not registered")
	return nil
}

// A restart that rebuilt from flag defaults would push on the machine you ran
// pull from, and put base back on the ports a mapped stack was serving — both
// silently, on a colleague's machine.
func TestRestartRepeatsTheLastForward(t *testing.T) {
	cases := []struct {
		name     string
		last     *workspace.TunnelForward
		flags    map[string]string
		wantMode tunnel.Mode
		wantSaid string
		wantBase string
		wantSvcs string
	}{
		{
			name:     "nothing recorded still pushes",
			wantMode: tunnel.ModePush,
		},
		{
			name:     "direction of the last run wins over the default",
			last:     &workspace.TunnelForward{Mode: "pull"},
			wantMode: tunnel.ModePull,
			wantSaid: "pull",
		},
		{
			name:     "a mapped stack is re-established mapped",
			last:     &workspace.TunnelForward{Mode: "push", AsBase: "agent"},
			wantMode: tunnel.ModePush,
			wantSaid: "push --as-base agent",
			wantBase: "agent",
		},
		{
			name:     "an explicit direction beats the recorded one",
			last:     &workspace.TunnelForward{Mode: "pull", AsBase: "agent"},
			flags:    map[string]string{"mode": "push"},
			wantMode: tunnel.ModePush,
			wantSaid: "--as-base agent",
			wantBase: "agent",
		},
		{
			name:     "asking for --stacks does not drag --as-base along",
			last:     &workspace.TunnelForward{Mode: "push", AsBase: "agent"},
			flags:    map[string]string{"stacks": "true"},
			wantMode: tunnel.ModePush,
			wantSaid: "push",
		},
		{
			name:     "the service filter is repeated too",
			last:     &workspace.TunnelForward{Mode: "push", Services: "navexa-api"},
			wantMode: tunnel.ModePush,
			wantSaid: "push --services navexa-api",
			wantSvcs: "navexa-api",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := restartCommand(t)
			for k, v := range tc.flags {
				if err := cmd.Flags().Set(k, v); err != nil {
					t.Fatalf("set --%s: %v", k, err)
				}
			}

			mode, said, err := resumeLastForward(cmd, tc.last)
			if err != nil {
				t.Fatalf("resumeLastForward: %v", err)
			}
			if mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", mode, tc.wantMode)
			}
			if said != tc.wantSaid {
				t.Errorf("reported %q, want %q", said, tc.wantSaid)
			}
			if tunnelAsBaseFlag != tc.wantBase {
				t.Errorf("--as-base = %q, want %q", tunnelAsBaseFlag, tc.wantBase)
			}
			if tunnelServicesFlag != tc.wantSvcs {
				t.Errorf("--services = %q, want %q", tunnelServicesFlag, tc.wantSvcs)
			}
		})
	}
	restartCommand(t)
}

func TestRestartRejectsAnUnknownDirection(t *testing.T) {
	cmd := restartCommand(t)
	if err := cmd.Flags().Set("mode", "sideways"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resumeLastForward(cmd, nil); err == nil {
		t.Fatal("expected an error for --mode sideways")
	}
	restartCommand(t)
}

// --reclaim kills whatever holds the port on the far host, which may be a
// colleague's session. Inheriting it from a previous run would make a bare
// restart destructive without anyone typing the flag.
func TestRestartNeverInheritsReclaim(t *testing.T) {
	cmd := restartCommand(t)
	tunnelReclaimFlag = false
	if _, _, err := resumeLastForward(cmd, &workspace.TunnelForward{Mode: "push", AsBase: "agent"}); err != nil {
		t.Fatal(err)
	}
	if tunnelReclaimFlag {
		t.Error("restart inherited --reclaim")
	}
	restartCommand(t)
}
