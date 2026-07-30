package cmd

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/ports"
)

var portsCmd = &cobra.Command{
	Use:   "ports",
	Short: "Inspect and reclaim the ports this machine's services hold",
}

var portsFreeCmd = &cobra.Command{
	Use:   "free <port> [port...]",
	Short: "Kill whatever still listens on the given ports",
	Long: `Terminate the processes holding the named TCP ports, so a service can bind on
start. Each one gets SIGTERM, then SIGKILL only if it is still alive after a
grace period — a dev server killed outright leaves its own children behind,
which is the mess this is meant to prevent.

Generated Tiltfiles call this for a service that sets runtime.prep.freePorts,
passing that instance's OWN resolved ports: a stack frees the ports it was
allocated, base frees the ports it pins, and neither can reach the other's.

Run by hand it does what you ask, so check what holds a port first:

  devstack ports check 4200
  devstack ports free 4200`,
	Args:         cobra.MinimumNArgs(1),
	SilenceUsage: true,
	RunE:         runPortsFree,
}

var portsCheckCmd = &cobra.Command{
	Use:          "check <port> [port...]",
	Short:        "Show what is listening on the given ports, without killing anything",
	Args:         cobra.MinimumNArgs(1),
	SilenceUsage: true,
	RunE:         runPortsCheck,
}

func init() {
	rootCmd.AddCommand(portsCmd)
	portsCmd.AddCommand(portsFreeCmd)
	portsCmd.AddCommand(portsCheckCmd)
	portsFreeCmd.Flags().Duration("grace", 2*time.Second, "How long to wait after SIGTERM before SIGKILL")
	portsFreeCmd.Flags().Bool("quiet", false, "Only report ports that actually had a listener")
}

// privilegedPort is the boundary below which a listener is far more likely to be
// a system service than a dev server someone forgot to stop.
const privilegedPort = 1024

func parsePorts(args []string) ([]int, error) {
	out := make([]int, 0, len(args))
	for _, a := range args {
		p, err := strconv.Atoi(a)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("%q is not a TCP port", a)
		}
		if p < privilegedPort {
			return nil, fmt.Errorf("refusing to touch privileged port %d — devstack services do not run below %d", p, privilegedPort)
		}
		out = append(out, p)
	}
	return out, nil
}

func runPortsCheck(cmd *cobra.Command, args []string) error {
	wanted, err := parsePorts(args)
	if err != nil {
		return err
	}
	for _, p := range wanted {
		listeners, err := ports.Find(p)
		if err != nil {
			return err
		}
		if len(listeners) == 0 {
			fmt.Printf("%-6d free\n", p)
			continue
		}
		for _, l := range listeners {
			fmt.Printf("%-6d pid %-8d %-5s %s\n", p, l.PID, l.Stack, l.Command)
		}
	}
	return nil
}

func runPortsFree(cmd *cobra.Command, args []string) error {
	wanted, err := parsePorts(args)
	if err != nil {
		return err
	}
	grace, _ := cmd.Flags().GetDuration("grace")
	quiet, _ := cmd.Flags().GetBool("quiet")

	for _, p := range wanted {
		listeners, err := ports.Find(p)
		if err != nil {
			return err
		}
		if len(listeners) == 0 {
			if !quiet {
				fmt.Printf("port %d already free\n", p)
			}
			continue
		}
		for _, l := range listeners {
			// Say what is being killed before killing it. A silent reclaim of
			// someone else's process is indistinguishable from a crash.
			fmt.Printf("port %d held by pid %d (%s) — terminating\n", p, l.PID, l.Command)
			if err := ports.Kill(l, func() { time.Sleep(grace) }); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
		}
	}
	return nil
}
