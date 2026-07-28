package cmd

import (
	"errors"
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

Both commands run where you are sitting. What differs is which way the ports move:
  push   Run it on the machine the services are running on. Exposes those ports
         on the far host (ssh -R), which reaches them at localhost:<port>.
  pull   Run it on the machine you want to reach the services FROM. Brings the
         far host's ports back to this one (ssh -L).

The remote host/user are remembered per-workspace after the first run, so later
invocations can omit them:

  devstack tunnel push my-box.ts.net --user alice   # first time
  devstack tunnel push                              # reuses saved remote

Filter to specific services with --services. These are exact service names —
the ones 'devstack tunnel list' prints, not partial matches:

  devstack tunnel push my-box.ts.net --services navexa-api,navexa-frontend

By default only this workspace's base service ports are forwarded. The two stack
modes do different things and cannot be combined:

  devstack tunnel push my-box.ts.net --stacks          # every stack, each on its own port
  devstack tunnel push my-box.ts.net --as-base agent   # one stack, on base's ports

Add --otel to forward the observability UI as well, so the remote can read this
machine's traces at the same address you use locally:

  devstack tunnel push my-box.ts.net --otel

--as-base puts one stack on the ports base normally serves, so the far end
reaches that stack at the address it already knows and nothing over there needs
reconfiguring:

  devstack tunnel push my-box.ts.net --as-base agent
  # far end :4200 → here :20006, far end :63290 → here :20005`,
}

var (
	tunnelUserFlag     string
	tunnelServicesFlag string
	tunnelReclaimFlag  bool
	tunnelStacksFlag   bool
	tunnelAsBaseFlag   string
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
		Short: "Stop and re-establish the tunnels that were up",
		Long: `Stop this workspace's tunnels and bring them back.

With no flags it repeats the last successful push or pull — same direction, same
services, same stack mapping — and says what it is repeating. Otherwise a
restart after 'push --as-base agent' would quietly put base back on those ports,
and a restart on the machine you ran 'pull' from would reverse the direction.

Any flag you pass overrides the saved one:

  devstack tunnel restart                     # whatever ran last
  devstack tunnel restart --mode pull         # same services, other direction`,
		Args: cobra.MaximumNArgs(1),
		RunE: runTunnelRestart,
	}
	tunnelStopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop this workspace's tunnels, all of them or named ones",
		Long: `Stop forwards this workspace started.

With no --services, every tunnel for the workspace is stopped. Narrow it to the
ones you name to leave the rest up:

  devstack tunnel stop --services navexa-api
  devstack tunnel stop`,
		Args: cobra.NoArgs,
		RunE: runTunnelStop,
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
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, listCmd} {
		c.Flags().StringVar(&tunnelServicesFlag, "services", "", "Comma-separated exact service names to forward, as printed by 'devstack tunnel list' (default: all)")
	}
	listCmd.Flags().BoolVar(&tunnelStacksFlag, "stacks", false, "Include every active feature stack, each on its own port — previews what push --stacks would forward")
	tunnelStopCmd.Flags().StringVar(&tunnelServicesFlag, "services", "", "Comma-separated exact service names whose tunnels to stop (default: all of this workspace's)")
	for _, c := range []*cobra.Command{pushCmd, restartCmd} {
		c.Flags().BoolVar(&tunnelReclaimFlag, "reclaim", false,
			"Kill whatever already holds these ports on the far host before forwarding. Destructive: it may belong to a colleague or another stack, not you. Check first with: ssh <host> 'ss -ltnp | grep <port>'")
		c.Flags().BoolVar(&tunnelStacksFlag, "stacks", false,
			"Also forward every active feature stack, each on its OWN allocated port. Cannot be combined with --as-base.")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, listCmd} {
		c.Flags().StringVar(&tunnelAsBaseFlag, "as-base", "",
			"Put ONE feature stack on base's ports, so the far end reaches that stack at the addresses base normally uses. Cannot be combined with --stacks.")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, listCmd} {
		c.Flags().BoolVar(&tunnelOtelFlag, "otel", false,
			"Also forward the observability UI, so the telemetry is readable from the other end at the same address you use here")
	}
	restartCmd.Flags().String("mode", "", "Direction to re-establish: push or pull (default: the direction of the last run, else push)")
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
	if err == nil {
		return svcs, true
	}
	// Only a failure to reach the daemon means the daemon is down; anything else
	// is the caller's answer to give, not a stale diagnosis.
	if errors.Is(err, errTunnelDaemon) {
		fmt.Printf("%s  ·  ", color.New(color.Bold).Sprint(ws.Name))
		color.New(color.FgYellow).Println("dev daemon not running")
		fmt.Println("  Run: devstack workspace up")
		return nil, false
	}
	color.New(color.FgRed).Printf("  %v\n", err)
	return nil, false
}

// errTunnelDaemon marks the one failure that means "start the daemon".
var errTunnelDaemon = errors.New("dev daemon unreachable")

// discoverTunnelServices queries Tilt and returns the services/ports to forward,
// honouring the --services filter. Results are sorted by port for stable output.
func discoverTunnelServices(ws *workspace.Workspace) ([]tunnel.Service, error) {
	if tunnelAsBaseFlag != "" && tunnelStacksFlag {
		return nil, fmt.Errorf("--as-base and --stacks ask for different things: --as-base %s puts that one stack on base's ports, --stacks forwards every stack on its own ports. Pick one", tunnelAsBaseFlag)
	}

	view, err := tilt.NewClient("localhost", ws.TiltPort).GetView()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errTunnelDaemon, err)
	}
	filter := map[string]bool{}
	for _, s := range strings.Split(tunnelServicesFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			filter[s] = true
		}
	}
	if tunnelAsBaseFlag != "" {
		mapped, unmapped, err := tunnel.StackOnBasePorts(view, filter, ws.Name, tunnelAsBaseFlag)
		if len(unmapped) > 0 {
			color.New(color.Faint).Printf("  not mapped (no base port to map onto): %s\n", strings.Join(unmapped, ", "))
		}
		return mapped, err
	}

	svcs := tunnel.Discover(view, filter, ws.Name, tunnelStacksFlag)
	if len(filter) > 0 && len(svcs) == 0 {
		return nil, fmt.Errorf("no service matches %s. Names must be exact; 'devstack tunnel list' prints the ones this workspace has", tunnelServicesFlag)
	}
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

// serviceNames joins service names for a one-line summary.
func serviceNames(svcs []tunnel.Service) string {
	out := make([]string, len(svcs))
	for i, s := range svcs {
		out[i] = s.Name
	}
	return strings.Join(out, ", ")
}

// tunnelPortLabel renders a forward's ports, naming both ends when they differ
// so a mapped forward does not read as a service on the wrong port.
func tunnelPortLabel(s tunnel.Service) string {
	if !s.Mapped() {
		return fmt.Sprintf(":%d", s.Port)
	}
	return fmt.Sprintf("remote :%d → local :%d", s.RemotePort, s.Port)
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
		// Naming every stopped service buries the forwards you asked for. A
		// count is enough, unless you named services yourself, in which case a
		// specific absence is the answer to the question you asked.
		if len(idle) > 0 {
			if tunnelServicesFlag != "" {
				color.New(color.Faint).Printf("  not serving, skipped: %s\n", serviceNames(idle))
			} else {
				color.New(color.Faint).Printf("  %d not serving, skipped\n", len(idle))
			}
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
	var started int
	for _, s := range svcs {
		pid, err := tunnel.Launch(ws.Name, mode, sshUser, host, s.Port, s.Far())
		if err != nil {
			clashed = true
			color.New(color.FgRed).Printf("  [FAILED]  %-30s %s  (%v)\n", s.Name, tunnelPortLabel(s), err)
			continue
		}
		started++
		fmt.Printf("  [started] %-30s %s  (pid %d)\n", s.Name, tunnelPortLabel(s), pid)
	}
	// Record the shape of what is now up so `restart` re-establishes this rather
	// than the flag defaults, which point the other way.
	if started > 0 {
		if serr := workspace.UpdateTunnelForward(ws.Name, workspace.TunnelForward{
			Mode:     string(mode),
			Services: tunnelServicesFlag,
			Stacks:   tunnelStacksFlag,
			AsBase:   tunnelAsBaseFlag,
			Otel:     tunnelOtelFlag,
		}); serr != nil {
			fmt.Printf("  warning: could not save what was forwarded: %v\n", serr)
		}
	}
	if clashed && mode == tunnel.ModePush && !tunnelReclaimFlag {
		fmt.Printf("\n  See what holds it:  ssh %s 'ss -ltnp | grep <port>'\n"+
			"  A forward fails when something already holds the port on %s — often a stale\n"+
			"  forward of your own, but it may belong to another stack. Check the remote, or\n"+
			"  re-run with --reclaim to kill whatever holds these ports there.\n", host, host)
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

	if wanted := tunnelServiceFilter(); len(wanted) > 0 {
		var kept []int
		for _, port := range ports {
			if wanted[bareServiceName(names[port])] {
				kept = append(kept, port)
			}
		}
		if len(kept) == 0 {
			return fmt.Errorf("no running tunnel matches %s — 'devstack tunnel status' lists what is up", tunnelServicesFlag)
		}
		ports = kept
	}
	fmt.Println("Stopping tunnels...")
	for _, port := range ports {
		tunnel.KillPort(ws.Name, port)
		fmt.Printf("  [stopped] %-30s :%d\n", portLabel(names, port), port)
	}
	return nil
}

// tunnelServiceFilter parses --services into a set, or nil when unset.
func tunnelServiceFilter() map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Split(tunnelServicesFlag, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// bareServiceName strips a label's ':stack' suffix so a stack's forward matches
// the service it is a copy of.
func bareServiceName(label string) string {
	if i := strings.IndexByte(label, ':'); i >= 0 {
		return label[:i]
	}
	return label
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

// resumeLastForward fills in the flags the caller left off from the last
// successful push or pull, so a bare restart re-establishes what was running.
// Anything given on the command line wins, and --reclaim is never restored: it
// kills whatever holds the port on the far host, which is a decision to take
// each time rather than inherit. Returns the direction and a description of
// what was restored, empty when nothing was.
func resumeLastForward(cmd *cobra.Command, last *workspace.TunnelForward) (tunnel.Mode, string, error) {
	modeStr, _ := cmd.Flags().GetString("mode")
	var restored []string
	if last != nil {
		// The two stack modes exclude each other, so naming either one means the
		// caller is choosing between them and neither should be inherited.
		stackModeGiven := cmd.Flags().Changed("stacks") || cmd.Flags().Changed("as-base")
		if modeStr == "" && last.Mode != "" {
			modeStr = last.Mode
			restored = append(restored, last.Mode)
		}
		if !cmd.Flags().Changed("services") && last.Services != "" {
			tunnelServicesFlag = last.Services
			restored = append(restored, "--services "+last.Services)
		}
		if !stackModeGiven && last.Stacks {
			tunnelStacksFlag = true
			restored = append(restored, "--stacks")
		}
		if !stackModeGiven && last.AsBase != "" {
			tunnelAsBaseFlag = last.AsBase
			restored = append(restored, "--as-base "+last.AsBase)
		}
		if !cmd.Flags().Changed("otel") && last.Otel {
			tunnelOtelFlag = true
			restored = append(restored, "--otel")
		}
	}
	if modeStr == "" {
		modeStr = string(tunnel.ModePush)
	}
	mode := tunnel.Mode(modeStr)
	if mode != tunnel.ModePush && mode != tunnel.ModePull {
		return "", "", fmt.Errorf("--mode must be push or pull, got %q", modeStr)
	}
	return mode, strings.Join(restored, " "), nil
}

func runTunnelRestart(cmd *cobra.Command, args []string) error {
	ws, err := tunnelContext()
	if err != nil {
		return err
	}
	mode, restored, err := resumeLastForward(cmd, ws.TunnelLast)
	if err != nil {
		return err
	}
	if restored != "" {
		color.New(color.Faint).Printf("  repeating last run: %s\n", restored)
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
