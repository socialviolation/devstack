package cmd

import (
	"fmt"
	osuser "os/user"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tunnel"
	"github.com/socialviolation/devstack/internal/workspace"
)

var tunnelCmd = &cobra.Command{
	Use:   "tunnel",
	Short: "Forward workspace service ports over SSH to/from a remote host",
	Long: `Discover the running services in this workspace and forward their ports over SSH.

Two modes:
  push   Run on THIS machine. Exposes local service ports on a remote host
         (ssh -R), so the remote can reach them on localhost:<port>.
  pull   Run on a REMOTE machine. Pulls ports from a source machine back to
         here (ssh -L).

The remote host/user are remembered per-workspace after the first run, so later
invocations can omit them:

  devstack tunnel push my-box.ts.net --user alice   # first time
  devstack tunnel push                              # reuses saved remote

Filter to specific services with --services:

  devstack tunnel push my-box.ts.net --services api,frontend`,
}

var (
	tunnelUserFlag     string
	tunnelServicesFlag string
)

func init() {
	rootCmd.AddCommand(tunnelCmd)

	pushCmd := &cobra.Command{
		Use:   "push [host]",
		Short: "Expose local service ports on a remote host (ssh -R)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  func(c *cobra.Command, a []string) error { return runTunnelForward(tunnel.ModePush, a) },
	}
	pullCmd := &cobra.Command{
		Use:   "pull [host]",
		Short: "Pull ports from a source machine back to here (ssh -L)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  func(c *cobra.Command, a []string) error { return runTunnelForward(tunnel.ModePull, a) },
	}
	restartCmd := &cobra.Command{
		Use:   "restart [host]",
		Short: "Stop and re-establish all tunnels",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runTunnelRestart,
	}
	tunnelStopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop all tunnels for this workspace",
		Args:  cobra.NoArgs,
		RunE:  runTunnelStop,
	}
	tunnelStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show tunnel status for this workspace",
		Args:  cobra.NoArgs,
		RunE:  runTunnelStatus,
	}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List discovered services and ports (no SSH)",
		Args:  cobra.NoArgs,
		RunE:  runTunnelList,
	}
	setRemoteCmd := &cobra.Command{
		Use:   "set-remote <host>",
		Short: "Save the default SSH remote (host/user) for this workspace",
		Long: `Persist the default tunnel remote for this workspace so push/pull can be run
without arguments. Stored per-machine in the devstack registry — it is not
committed to the repo.

  devstack tunnel set-remote macbook --user nickfreemantle`,
		Args: cobra.ExactArgs(1),
		RunE: runTunnelSetRemote,
	}

	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, setRemoteCmd} {
		c.Flags().StringVar(&tunnelUserFlag, "user", "", "SSH user (default: saved user or current user)")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd} {
		c.Flags().StringVar(&tunnelServicesFlag, "services", "", "Comma-separated service names to forward (default: all)")
	}
	restartCmd.Flags().String("mode", string(tunnel.ModePush), "Direction to re-establish: push or pull")
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, tunnelStopCmd, tunnelStatusCmd, listCmd, setRemoteCmd} {
		// Runtime failures (daemon down, unreachable remote) shouldn't dump the
		// full usage block — the messages are self-explanatory.
		c.SilenceUsage = true
		tunnelCmd.AddCommand(c)
	}
}

// tunnelContext resolves the active workspace, its live Tilt port, and asserts a
// local environment. Tunnels only make sense against the local dev stack.
func tunnelContext() (*workspace.Workspace, error) {
	ws, env, envName, err := resolveWorkspaceAndEnv()
	if err != nil {
		return nil, err
	}
	if err := requireLocalEnv(envName, env); err != nil {
		return nil, err
	}
	if actual := workspace.ResolvePort(ws.Name); actual != 0 {
		ws.TiltPort = actual
	}
	return ws, nil
}

// tunnelServices discovers services, or — when the dev daemon isn't running —
// prints the same gentle hint `devstack status` uses and reports ok=false so the
// caller can exit cleanly (no raw Tilt error, no usage dump).
func tunnelServices(ws *workspace.Workspace) (svcs []tunnel.Service, ok bool) {
	svcs, err := discoverTunnelServices(ws)
	if err != nil {
		fmt.Printf("%s  ·  ", color.New(color.Bold).Sprint(ws.Name))
		color.New(color.FgYellow).Println("dev daemon not running")
		fmt.Println("  Run: devstack up")
		return nil, false
	}
	return svcs, true
}

// discoverTunnelServices queries Tilt and returns the services/ports to forward,
// honouring the --services filter. Results are sorted by port for stable output.
func discoverTunnelServices(ws *workspace.Workspace) ([]tunnel.Service, error) {
	view, err := tilt.NewClient("localhost", ws.TiltPort).GetView()
	if err != nil {
		return nil, err
	}
	filter := map[string]bool{}
	for _, s := range strings.Split(tunnelServicesFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			filter[s] = true
		}
	}
	svcs := tunnel.Discover(view, filter)
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Port < svcs[j].Port })
	return svcs, nil
}

// resolveRemote determines the ssh host/user from args/flags, falling back to the
// saved per-workspace remote. The returned explicit flag reports whether the
// caller supplied a value (host arg or --user), so it is only persisted once the
// connection is known to work.
func resolveRemote(ws *workspace.Workspace, args []string) (host, user string, explicit bool, err error) {
	host = ws.TunnelHost
	if len(args) == 1 && args[0] != "" {
		host = args[0]
		explicit = true
	}
	if host == "" {
		return "", "", false, fmt.Errorf("no remote host given and none saved for %q\nUsage: devstack tunnel push <host> [--user <user>]", ws.Name)
	}

	user = tunnelUserFlag
	if user == "" {
		user = ws.TunnelUser
	}
	if user == "" {
		if u, uerr := currentUser(); uerr == nil {
			user = u
		}
	}
	if user == "" {
		return "", "", false, fmt.Errorf("could not determine SSH user; pass --user")
	}
	if tunnelUserFlag != "" {
		explicit = true
	}
	return host, user, explicit, nil
}

// printSSHGuidance explains, gently, why a tunnel can't be opened and how to fix
// it. Tunnels rely on key-based SSH — the most common failure is simply that the
// user's public key isn't on the remote yet.
func printSSHGuidance(user, host string, cause error) {
	color.New(color.FgYellow, color.Bold).Printf("Can't open an SSH session to %s@%s.\n", user, host)
	if cause != nil {
		color.New(color.Faint).Printf("  ssh: %s\n", cause)
	}
	fmt.Println()
	fmt.Println("devstack tunnels use key-based SSH — no passwords, no prompts. To enable it:")
	fmt.Printf("  1. Check you can reach the host:   %s\n", color.New(color.Bold).Sprintf("ssh %s@%s", user, host))
	fmt.Printf("  2. Install your key on the remote: %s\n", color.New(color.Bold).Sprintf("ssh-copy-id %s@%s", user, host))
	fmt.Printf("  3. Re-run:                         %s\n", color.New(color.Bold).Sprintf("devstack tunnel push %s", host))
	fmt.Println()
	color.New(color.Faint).Println("  Tip: for a saved SSH config alias, use it as the host (e.g. `devstack tunnel push mybox`).")
}

// currentUser returns the current OS username.
func currentUser() (string, error) {
	u, err := osuser.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func runTunnelForward(mode tunnel.Mode, args []string) error {
	ws, err := tunnelContext()
	if err != nil {
		return err
	}
	host, sshUser, explicit, err := resolveRemote(ws, args)
	if err != nil {
		return err
	}
	svcs, ok := tunnelServices(ws)
	if !ok {
		return nil
	}
	if len(svcs) == 0 {
		fmt.Println("No services with forwardable ports found.")
		return nil
	}

	// Push forwards LOCAL ports to the remote, so only forward ports that are
	// actually serving traffic here — a dead port would just create a broken
	// forward on the remote.
	if mode == tunnel.ModePush {
		serving, idle := tunnel.PartitionServing(svcs)
		for _, s := range idle {
			color.New(color.Faint).Printf("  [skip]    %-30s :%d  (not serving)\n", s.Name, s.Port)
		}
		svcs = serving
		if len(svcs) == 0 {
			fmt.Println("No serving ports to forward right now. Start the services first (devstack start).")
			return nil
		}
	}

	// Preflight: confirm key-based SSH works before touching any ports. This keeps
	// failures gentle (one clear message, not a wall of failed forwards) and
	// teaches how to enable access when keys aren't set up on the remote.
	if err := tunnel.CheckConnectivity(sshUser, host); err != nil {
		printSSHGuidance(sshUser, host, err)
		return nil
	}

	// The remote works — remember it (only if the user named it explicitly).
	if explicit {
		if serr := workspace.UpdateTunnelRemote(ws.Name, host, sshUser); serr != nil {
			fmt.Printf("  warning: could not save remote: %v\n", serr)
		}
	}

	fmt.Printf("%s tunnels → %s@%s\n", strings.ToUpper(string(mode)), sshUser, host)

	if mode == tunnel.ModePush {
		ports := make([]int, len(svcs))
		for i, s := range svcs {
			ports[i] = s.Port
		}
		fmt.Printf("  Reclaiming ports on %s...\n", host)
		tunnel.ReclaimRemote(sshUser, host, ports)
	}

	for _, s := range svcs {
		pid, err := tunnel.Launch(ws.Name, mode, sshUser, host, s.Port)
		if err != nil {
			color.New(color.FgRed).Printf("  [FAILED]  %-30s :%d  (%v)\n", s.Name, s.Port, err)
			continue
		}
		fmt.Printf("  [started] %-30s :%d  (pid %d)\n", s.Name, s.Port, pid)
	}
	return nil
}

func runTunnelStop(cmd *cobra.Command, args []string) error {
	ws, err := tunnelContext()
	if err != nil {
		return err
	}
	svcs, ok := tunnelServices(ws)
	if !ok {
		return nil
	}
	fmt.Println("Stopping tunnels...")
	for _, s := range svcs {
		tunnel.KillPort(ws.Name, s.Port)
		fmt.Printf("  [stopped] %-30s :%d\n", s.Name, s.Port)
	}
	return nil
}

func runTunnelRestart(cmd *cobra.Command, args []string) error {
	modeStr, _ := cmd.Flags().GetString("mode")
	mode := tunnel.Mode(modeStr)
	if mode != tunnel.ModePush && mode != tunnel.ModePull {
		return fmt.Errorf("--mode must be push or pull, got %q", modeStr)
	}
	if err := runTunnelStop(cmd, args); err != nil {
		return err
	}
	return runTunnelForward(mode, args)
}

func runTunnelStatus(cmd *cobra.Command, args []string) error {
	ws, err := tunnelContext()
	if err != nil {
		return err
	}
	svcs, ok := tunnelServices(ws)
	if !ok {
		return nil
	}
	if ws.TunnelHost != "" {
		color.New(color.Faint).Printf("remote: %s@%s\n", ws.TunnelUser, ws.TunnelHost)
	}
	fmt.Println("Tunnel status:")
	for _, s := range svcs {
		if tunnel.IsUp(ws.Name, s.Port) {
			color.New(color.FgGreen).Printf("  [up]   ")
		} else {
			color.New(color.Faint).Printf("  [down] ")
		}
		fmt.Printf("%-30s :%d  devstack:%s\n", s.Name, s.Port, s.Runtime)
	}
	return nil
}

func runTunnelSetRemote(cmd *cobra.Command, args []string) error {
	ws, err := tunnelContext()
	if err != nil {
		return err
	}
	host := args[0]
	user := tunnelUserFlag
	if user == "" {
		user = ws.TunnelUser
	}
	if user == "" {
		if u, uerr := currentUser(); uerr == nil {
			user = u
		}
	}
	if err := workspace.UpdateTunnelRemote(ws.Name, host, user); err != nil {
		return err
	}
	fmt.Printf("✓ Saved tunnel remote for '%s': %s@%s\n", ws.Name, user, host)
	color.New(color.Faint).Printf("  Run: devstack tunnel push\n")
	return nil
}

func runTunnelList(cmd *cobra.Command, args []string) error {
	ws, err := tunnelContext()
	if err != nil {
		return err
	}
	svcs, ok := tunnelServices(ws)
	if !ok {
		return nil
	}
	fmt.Println("Devstack services:")
	for _, s := range svcs {
		fmt.Printf("  %-30s :%d  (%s)\n", s.Name, s.Port, s.Runtime)
	}
	return nil
}
