package cmd

import (
	"fmt"
	osuser "os/user"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/stack"
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

  devstack tunnel push my-box.ts.net --services api,frontend

By default only this workspace's base service ports are forwarded. Add --stacks
to also forward the ports of its active feature stacks:

  devstack tunnel push my-box.ts.net --stacks

Add --otel to forward the observability UI as well, so the remote can read this
machine's traces at the same address you use locally:

  devstack tunnel push my-box.ts.net --otel

--stacks forwards each stack instance on its own allocated port. --stack <name>
does something different: it puts ONE stack on the ports base normally serves,
so the remote reaches that stack at the address it already knows, without
reconfiguring anything over there:

  devstack tunnel push my-box.ts.net --stack agent
  # remote :4200 → local :20006, remote :63290 → local :20005`,
}

var (
	tunnelUserFlag     string
	tunnelServicesFlag string
	tunnelReclaimFlag  bool
	tunnelStacksFlag   bool
	tunnelStackFlag    string
	tunnelOtelFlag     bool
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
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd} {
		c.Flags().StringVar(&tunnelUserFlag, "user", "", "SSH user (default: saved user or current user)")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd} {
		c.Flags().StringVar(&tunnelServicesFlag, "services", "", "Comma-separated service names to forward (default: all)")
	}
	for _, c := range []*cobra.Command{pushCmd, restartCmd} {
		c.Flags().BoolVar(&tunnelReclaimFlag, "reclaim", false,
			"Kill whatever already holds these ports on the remote before forwarding (destructive: will tear down other stacks' forwards)")
		c.Flags().BoolVar(&tunnelStacksFlag, "stacks", false,
			"Also forward this workspace's active feature-stack service ports, each on its own port")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, listCmd} {
		c.Flags().StringVar(&tunnelStackFlag, "stack", "",
			"Forward one feature stack onto the base ports: the remote reaches the stack's instances at the addresses base normally uses")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, listCmd} {
		c.Flags().BoolVar(&tunnelOtelFlag, "otel", false,
			"Also forward the observability UI, so the remote can read this machine's traces")
	}
	restartCmd.Flags().String("mode", string(tunnel.ModePush), "Direction to re-establish: push or pull")
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, tunnelStopCmd, tunnelStatusCmd, listCmd} {
		// Runtime failures (daemon down, unreachable remote) shouldn't dump the
		// full usage block — the messages are self-explanatory.
		c.SilenceUsage = true
		tunnelCmd.AddCommand(c)
	}
}

// tunnelContext resolves the active workspace, its live Tilt port, and asserts a
// local environment. Tunnels only make sense against the local dev stack.
func tunnelContext() (*workspace.Workspace, error) {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return nil, err
	}
	ws.TiltPort = workspace.HostTiltPort
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
		fmt.Println("  Run: devstack workspace up")
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
	if tunnelStackFlag != "" {
		return stackOnBasePorts(view, filter, ws, tunnelStackFlag)
	}

	svcs := tunnel.Discover(view, filter, ws.Name, tunnelStacksFlag)
	if tunnelOtelFlag {
		ui, reason, ok := otelUI(ws)
		if ok {
			svcs = append(svcs, ui)
		} else {
			fmt.Printf("  warning: %s.\n", reason)
		}
	}
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Port < svcs[j].Port })
	return svcs, nil
}

// tunnelPortLabel renders a forward's ports, naming both ends when they differ
// so a mapped forward does not read as a service on the wrong port.
func tunnelPortLabel(s tunnel.Service) string {
	if !s.Mapped() {
		return fmt.Sprintf(":%d", s.Port)
	}
	return fmt.Sprintf("remote :%d → local :%d", s.RemotePort, s.Port)
}

// stackOnBasePorts maps one stack's instances onto the ports base normally
// serves, so the far end reaches the stack at the address it already knows. Each
// forward listens on base's port over there and lands on the stack's port here;
// a service the stack does not overlay is left out, since base already serves it.
func stackOnBasePorts(view *tilt.TiltView, filter map[string]bool, ws *workspace.Workspace, stackName string) ([]tunnel.Service, error) {
	rec, err := stack.FindStack(ws.Name, stackName)
	if err != nil {
		return nil, err
	}

	basePorts := map[string]int{}
	for _, s := range tunnel.Discover(view, nil, ws.Name, false) {
		if _, seen := basePorts[s.Service]; !seen {
			basePorts[s.Service] = s.Port
		}
	}

	var out []tunnel.Service
	var unmapped []string
	for _, s := range tunnel.Discover(view, filter, ws.Name, true) {
		if !strings.HasSuffix(s.Name, ":"+rec.Name) {
			continue
		}
		base, ok := basePorts[s.Service]
		if !ok {
			unmapped = append(unmapped, s.Service)
			continue
		}
		s.RemotePort = base
		out = append(out, s)
	}

	if len(unmapped) > 0 {
		color.New(color.Faint).Printf("  skipped (no base port to map onto): %s\n", strings.Join(unmapped, ", "))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("stack %q has no running services to forward — bring it up with: devstack stack up %s", rec.Name, rec.Name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RemotePort < out[j].RemotePort })
	return out, nil
}

// otelUI resolves the observability UI as a forwardable port. It is not a Tilt
// resource, so it is never discovered — it is added on --otel, and labelled
// everywhere else so a live forward for it is always named. Quiet by design:
// status and stop call this for labels and must not narrate.
func otelUI(ws *workspace.Workspace) (svc tunnel.Service, reason string, ok bool) {
	plugin := otel.For(ws)
	if plugin == nil {
		return tunnel.Service{}, "no observability backend configured", false
	}
	endpoint := plugin.QueryEndpoint(ws)
	if endpoint == "" {
		return tunnel.Service{}, fmt.Sprintf("backend %q has no local UI to forward — telemetry goes upstream instead", plugin.Name()), false
	}
	port := tunnel.PortFromURL(endpoint)
	if port == 0 {
		return tunnel.Service{}, fmt.Sprintf("could not read a port from the %s UI address %q", plugin.Name(), endpoint), false
	}
	return tunnel.Service{Name: "otel-ui (" + plugin.Name() + ")", Port: port, Runtime: "ok"}, "", true
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
			color.New(color.Faint).Printf("  [skip]    %-30s %s  (not serving)\n", s.Name, tunnelPortLabel(s))
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

	if mode == tunnel.ModePush && tunnelReclaimFlag {
		ports := make([]int, len(svcs))
		for i, s := range svcs {
			ports[i] = s.Far()
		}
		fmt.Printf("  Reclaiming ports on %s...\n", host)
		tunnel.ReclaimRemote(sshUser, host, ports)
	}

	var clashed bool
	for _, s := range svcs {
		pid, err := tunnel.Launch(ws.Name, mode, sshUser, host, s.Port, s.Far())
		if err != nil {
			clashed = true
			color.New(color.FgRed).Printf("  [FAILED]  %-30s %s  (%v)\n", s.Name, tunnelPortLabel(s), err)
			continue
		}
		fmt.Printf("  [started] %-30s %s  (pid %d)\n", s.Name, tunnelPortLabel(s), pid)
	}
	if clashed && mode == tunnel.ModePush && !tunnelReclaimFlag {
		fmt.Printf("\n  A forward fails when something already holds the port on %s — often a stale\n"+
			"  forward of your own, but it may belong to another stack. Check the remote, or\n"+
			"  re-run with --reclaim to kill whatever holds these ports there.\n", host)
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
	// Stop what is actually forwarding, not what happens to be discoverable now —
	// a forward outlives its service, and the observability UI is never a Tilt
	// resource at all.
	ports := tunnel.TrackedPorts(ws.Name)
	if len(ports) == 0 {
		fmt.Println("No tunnels running for this workspace.")
		return nil
	}
	names := portLabels(ws, svcs)
	fmt.Println("Stopping tunnels...")
	for _, port := range ports {
		tunnel.KillPort(ws.Name, port)
		fmt.Printf("  [stopped] %-30s :%d\n", portLabel(names, port), port)
	}
	return nil
}

// portLabels maps a forwarded port to the name to show for it. Ports whose
// service is no longer discoverable still have a live forward, so they get a
// placeholder rather than being dropped from the report.
func portLabels(ws *workspace.Workspace, svcs []tunnel.Service) map[int]string {
	labels := map[int]string{}
	if ui, _, ok := otelUI(ws); ok {
		labels[ui.Port] = ui.Name
	}
	// A stack's allocated ports are recorded, so a forward of one stays
	// identifiable even when the tool was called without --stack, or with the
	// daemon down.
	if recs, err := stack.LoadStore(ws.Name); err == nil {
		for _, rec := range recs {
			for key, port := range rec.Ports {
				svc := key
				if i := strings.IndexByte(key, '/'); i >= 0 {
					svc = key[:i]
				}
				labels[port] = svc + ":" + rec.Name
			}
		}
	}
	for _, s := range svcs {
		labels[s.Port] = s.Name
	}
	return labels
}

func portLabel(labels map[int]string, port int) string {
	if name, ok := labels[port]; ok {
		return name
	}
	return "(no longer discovered)"
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
	// Report every discovered service plus any port still forwarding that
	// discovery no longer covers, so a live tunnel is never invisible.
	runtimes := map[int]string{}
	for _, s := range svcs {
		runtimes[s.Port] = s.Runtime
	}
	labels := portLabels(ws, svcs)
	seen := map[int]bool{}
	var ports []int
	add := func(port int) {
		if !seen[port] {
			seen[port] = true
			ports = append(ports, port)
		}
	}
	for _, s := range svcs {
		add(s.Port)
	}
	for _, port := range tunnel.TrackedPorts(ws.Name) {
		add(port)
	}
	sort.Ints(ports)

	fmt.Println("Tunnel status:")
	for _, port := range ports {
		if tunnel.IsUp(ws.Name, port) {
			color.New(color.FgGreen).Printf("  [up]   ")
		} else {
			color.New(color.Faint).Printf("  [down] ")
		}
		runtime := runtimes[port]
		if runtime == "" {
			runtime = "-"
		}
		fmt.Printf("%-30s :%d  devstack:%s\n", portLabel(labels, port), port, runtime)
	}
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
		fmt.Printf("  %-30s %s  (%s)\n", s.Name, tunnelPortLabel(s), s.Runtime)
	}
	return nil
}
