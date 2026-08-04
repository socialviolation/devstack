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
	Short: "Forward this workspace's service ports over SSH, to or from a remote host",
	Long: `Discover the running services of this workspace, and forward their ports over SSH.

Both commands run where you sit. They differ in the direction the ports move:
  push   Run it on the machine that runs the services. It exposes those ports on
         the far host (ssh -R), which reaches them at localhost:<port>.
  pull   Run it on the machine you want to reach the services FROM. It brings the
         far host's ports back to this machine (ssh -L).

devstack remembers the remote host and the remote user for each workspace after
the first run. A later run can leave them out:

  devstack tunnel push my-box.ts.net --user alice   # first time
  devstack tunnel push                              # reuses the saved remote

To forward only some services, pass --service. The names are exact. They are the
names that 'devstack tunnel status' prints, not partial matches:

  devstack tunnel push my-box.ts.net --service navexa-api,navexa-frontend

By default devstack forwards only this workspace's base service ports. The two
stack modes do different things, and you can not combine them:

  devstack tunnel push my-box.ts.net --stacks          # every stack, each on its own port
  devstack tunnel push my-box.ts.net --as-base agent   # one stack, on base's ports

Pass --otel to forward the observability UI too. The remote then reads this
machine's traces at the same address that you use here:

  devstack tunnel push my-box.ts.net --otel

--as-base puts one stack on the ports that base normally serves. The far end then
reaches that stack at the address it already knows, and nothing over there needs
new configuration:

  devstack tunnel push my-box.ts.net --as-base agent
  # far end :4200 → here :20006, far end :63290 → here :20005`,
}

var (
	tunnelUserFlag     string
	tunnelServicesFlag []string
	tunnelReclaimFlag  bool
	tunnelStacksFlag   bool
	tunnelAsBaseFlag   string
	tunnelOtelFlag     bool
	tunnelPlannedFlag  bool
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
		Long: `Stop this workspace's tunnels, and bring them back.

With no flags, this command repeats the last successful push or pull. It uses the
same direction, the same services and the same stack mapping, and it says what it
repeats. Without that record, a restart after 'push --as-base agent' puts base
back on those ports and says nothing. Without that record, a restart on the
machine you ran 'pull' from reverses the direction.

A flag that you pass overrides the saved one:

  devstack tunnel restart                     # whatever ran last
  devstack tunnel restart --mode pull         # same services, other direction`,
		Args: cobra.MaximumNArgs(1),
		RunE: runTunnelRestart,
	}
	tunnelStopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop this workspace's tunnels, all of them or named ones",
		Long: `Stop the forwards that this workspace started.

With no --service, devstack stops every tunnel of the workspace. To leave the
rest up, name the ones to stop:

  devstack tunnel stop --service navexa-api
  devstack tunnel stop`,
		Args: cobra.NoArgs,
		RunE: runTunnelStop,
	}
	tunnelStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "HERE: which of this workspace's ports are forwarded. With --planned: which ones a push or pull covers",
		Long: `Show every forward that this workspace has up. devstack also shows any port that
still forwards but that discovery no longer covers, so a live tunnel is never
invisible.

This command reads THIS machine, and it talks to no remote. To ask the far host
what already listens on these ports, run 'devstack tunnel check'.

With --planned it answers a different question: what a push or a pull forwards
from here, discovered from the running services. That reads nothing over SSH, and
it starts nothing. It is a preview, not a report of what is up.

  devstack tunnel status
  devstack tunnel status --planned --stacks`,
		Args: cobra.NoArgs,
		RunE: runTunnelStatus,
	}
	tunnelStatusCmd.Flags().BoolVar(&tunnelPlannedFlag, "planned", false,
		"Preview the ports a push or a pull covers (discovery only, no SSH). This is not a report of what is forwarded now")
	checkCmd := &cobra.Command{
		Use:   "check [host]",
		Short: "THERE: what already holds these ports on the remote host (one SSH round trip)",
		Long: `Ask the remote host what listens on the ports that a push binds.

This command reads the REMOTE host over SSH. To see what this machine forwards,
run 'devstack tunnel status'.

This is the counterpart to --reclaim. Reclaim can not tell a stale forward of
yours from a live one that another stack owns. Find out whose it is before you
kill it. To narrow the ports, pass --service.

  devstack tunnel check
  devstack tunnel check --service navexa-api,nxOrbit`,
		Args: cobra.MaximumNArgs(1),
		RunE: runTunnelCheck,
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd} {
		c.Flags().StringVar(&tunnelUserFlag, "user", "", "SSH user. Default: the saved user, or the current user")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, tunnelStatusCmd, checkCmd} {
		c.Flags().StringSliceVar(&tunnelServicesFlag, "service", nil, "Service to act on. Repeat the flag, or give a comma-separated list. The names are exact, as 'devstack tunnel status' prints them. Default: every service.")
	}
	tunnelStatusCmd.Flags().BoolVar(&tunnelStacksFlag, "stacks", false, "With --planned: include every active feature stack, each on its own port. This previews what push --stacks forwards")
	tunnelStopCmd.Flags().StringSliceVar(&tunnelServicesFlag, "service", nil, "Service whose tunnel to stop. Repeat the flag, or give a comma-separated list. Default: every tunnel of this workspace.")
	for _, c := range []*cobra.Command{pushCmd, restartCmd} {
		c.Flags().BoolVar(&tunnelReclaimFlag, "reclaim", false,
			"DESTRUCTIVE: before it forwards, devstack kills whatever already holds these ports on the far host. What it kills can belong to a colleague, or to another stack. devstack reclaims only the ports it forwards, so narrow the blast radius with --service. To see what holds them first, run: devstack tunnel check <host>")
		c.Flags().BoolVar(&tunnelStacksFlag, "stacks", false,
			"Also forward every active feature stack, each on its OWN allocated port. You can not combine it with --as-base.")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, tunnelStatusCmd} {
		c.Flags().StringVar(&tunnelAsBaseFlag, "as-base", "",
			"Put ONE feature stack on base's ports. The far end then reaches that stack at the addresses base normally uses. You can not combine it with --stacks.")
	}
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, tunnelStatusCmd} {
		c.Flags().BoolVar(&tunnelOtelFlag, "otel", false,
			"Also forward the observability UI. The other end then reads the telemetry at the same address that you use here")
	}
	restartCmd.Flags().String("mode", "", "Direction to re-establish: push or pull. Default: the direction of the last run, or push")
	for _, c := range []*cobra.Command{pushCmd, pullCmd, restartCmd, tunnelStopCmd, tunnelStatusCmd, checkCmd} {
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

// tunnelServices discovers services, or — when the dev daemon is not running —
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
// honouring the --service filter. Results are sorted by port for stable output.
func discoverTunnelServices(ws *workspace.Workspace) ([]tunnel.Service, error) {
	if tunnelAsBaseFlag != "" && tunnelStacksFlag {
		return nil, fmt.Errorf("--as-base and --stacks ask for different things. --as-base %s puts that one stack on base's ports. --stacks forwards every stack on its own ports. Pick one", tunnelAsBaseFlag)
	}

	view, err := tilt.NewClient("localhost", ws.TiltPort).GetView()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errTunnelDaemon, err)
	}
	filter := map[string]bool{}
	for _, s := range tunnelServicesFlag {
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
		return nil, fmt.Errorf("no service matches %s. The names must be exact. 'devstack tunnel status --planned' prints the ones this workspace has", strings.Join(tunnelServicesFlag, ", "))
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
		return tunnel.Service{}, "no observability backend is configured", false
	}
	endpoint := plugin.QueryEndpoint(ws)
	if endpoint == "" {
		return tunnel.Service{}, fmt.Sprintf("backend %q has no local UI to forward — telemetry goes upstream instead", plugin.Name()), false
	}
	port := tunnel.PortFromURL(endpoint)
	if port == 0 {
		return tunnel.Service{}, fmt.Sprintf("can not read a port from the %s UI address %q", plugin.Name(), endpoint), false
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
		return "", "", false, fmt.Errorf("you gave no remote host, and devstack has none saved for %q\nUsage: devstack tunnel push <host> [--user <user>]", ws.Name)
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
		return "", "", false, fmt.Errorf("devstack can not determine the SSH user. Pass --user")
	}
	if tunnelUserFlag != "" {
		explicit = true
	}
	return host, user, explicit, nil
}

// printSSHGuidance explains, gently, why a tunnel cannot be opened and how to fix
// it. Tunnels rely on key-based SSH — the most common failure is that the
// user's public key is not on the remote yet.
func printSSHGuidance(user, host string, cause error) {
	color.New(color.FgYellow, color.Bold).Printf("devstack can not open an SSH session to %s@%s.\n", user, host)
	if cause != nil {
		color.New(color.Faint).Printf("  ssh: %s\n", cause)
	}
	fmt.Println()
	fmt.Println("devstack tunnels use key-based SSH — no passwords, no prompts. To enable it:")
	fmt.Printf("  1. Make sure that you can reach the host: %s\n", color.New(color.Bold).Sprintf("ssh %s@%s", user, host))
	fmt.Printf("  2. Install your key on the remote:       %s\n", color.New(color.Bold).Sprintf("ssh-copy-id %s@%s", user, host))
	fmt.Printf("  3. Run this again:                       %s\n", color.New(color.Bold).Sprintf("devstack tunnel push %s", host))
	fmt.Println()
	color.New(color.Faint).Println("  Tip: if you have a saved SSH config alias, use it as the host (for example `devstack tunnel push mybox`).")
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
		fmt.Println("devstack found no service with a forwardable port.")
		return nil
	}

	// Push forwards LOCAL ports to the remote, so only forward ports that are
	// serving traffic here — a dead port would just create a broken
	// forward on the remote.
	if mode == tunnel.ModePush {
		serving, idle := tunnel.PartitionServing(svcs)
		// Naming every stopped service buries the forwards you asked for. A
		// count is enough, unless you named services yourself, in which case a
		// specific absence is the answer to the question you asked.
		if len(idle) > 0 {
			if len(tunnelServicesFlag) > 0 {
				color.New(color.Faint).Printf("  not serving, skipped: %s\n", serviceNames(idle))
			} else {
				color.New(color.Faint).Printf("  %d not serving, skipped\n", len(idle))
			}
		}
		svcs = serving
		if len(svcs) == 0 {
			fmt.Println("No port serves traffic right now. Start the services first: devstack service start <svc> --stack base")
			return nil
		}
	}

	// Preflight: confirm key-based SSH works before touching any ports. This keeps
	// failures gentle (one clear message, not a wall of failed forwards) and
	// teaches how to enable access when keys are not set up on the remote.
	if err := tunnel.CheckConnectivity(sshUser, host); err != nil {
		printSSHGuidance(sshUser, host, err)
		return nil
	}

	// The remote works — remember it (only if the user named it explicitly).
	if explicit {
		if serr := workspace.UpdateTunnelRemote(ws.Name, host, sshUser); serr != nil {
			fmt.Printf("  warning: devstack can not save the remote: %v\n", serr)
		}
	}

	fmt.Printf("%s tunnels → %s@%s\n", strings.ToUpper(string(mode)), sshUser, host)

	if mode == tunnel.ModePush && tunnelReclaimFlag {
		ports := make([]int, len(svcs))
		for i, s := range svcs {
			ports[i] = s.Far()
		}
		fmt.Printf("  devstack reclaims the ports on %s...\n", host)
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
			Services: strings.Join(tunnelServicesFlag, ","),
			Stacks:   tunnelStacksFlag,
			AsBase:   tunnelAsBaseFlag,
			Otel:     tunnelOtelFlag,
		}); serr != nil {
			fmt.Printf("  warning: devstack can not save what it forwarded: %v\n", serr)
		}
	}
	if clashed && mode == tunnel.ModePush && !tunnelReclaimFlag {
		fmt.Printf("\n  A forward fails when something already holds the port on %s. That is often a\n"+
			"  stale forward of your own. It can also belong to another stack.\n"+
			"  To see what holds it, run:  ssh %s 'ss -ltnp | grep <port>'\n"+
			"  To kill whatever holds these ports there, run this again with --reclaim.\n", host, host)
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
	// Stop what is forwarding, not what happens to be discoverable now —
	// a forward outlives its service, and the observability UI is never a Tilt
	// resource at all.
	ports := tunnel.TrackedPorts(ws.Name)
	if len(ports) == 0 {
		fmt.Println("No tunnel runs for this workspace.")
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
			return fmt.Errorf("no running tunnel matches %s — 'devstack tunnel status' lists what is up", strings.Join(tunnelServicesFlag, ", "))
		}
		ports = kept
	}
	fmt.Println("devstack stops the tunnels...")
	for _, port := range ports {
		tunnel.KillPort(ws.Name, port)
		fmt.Printf("  [stopped] %-30s :%d\n", portLabel(names, port), port)
	}
	return nil
}

// tunnelServiceFilter parses --service into a set, or nil when unset.
func tunnelServiceFilter() map[string]bool {
	out := map[string]bool{}
	for _, name := range tunnelServicesFlag {
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
		if !cmd.Flags().Changed("service") && last.Services != "" {
			tunnelServicesFlag = splitCSV(last.Services)
			restored = append(restored, "--service "+last.Services)
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
		color.New(color.Faint).Printf("  This repeats the last run: %s\n", restored)
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
	if tunnelPlannedFlag {
		return printPlannedForwards(svcs)
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

// printPlannedForwards renders what a push or pull would forward: discovery
// only, nothing consulted over SSH and nothing that is up. It answers a
// different question from the status report above it, which is why it takes a
// flag rather than a command of its own.
func printPlannedForwards(svcs []tunnel.Service) error {
	fmt.Println("Preview only — devstack starts nothing now. A push or a pull forwards these:")
	for _, s := range svcs {
		fmt.Printf("  %-30s %s  (%s)\n", s.Name, tunnelPortLabel(s), s.Runtime)
	}
	return nil
}

// runTunnelCheck reports what holds the far-host ports a push would bind. It
// changes nothing: --reclaim is the destructive counterpart, and this exists so
// the choice to use it is informed rather than hopeful.
func runTunnelCheck(cmd *cobra.Command, args []string) error {
	ws, err := tunnelContext()
	if err != nil {
		return err
	}
	host, sshUser, _, err := resolveRemote(ws, args)
	if err != nil {
		return err
	}

	svcs, err := discoverTunnelServices(ws)
	if err != nil {
		return err
	}
	if len(svcs) == 0 {
		fmt.Println("No port serves traffic right now. Start the services first: devstack service start <svc> --stack base")
		return nil
	}

	ports := make([]int, len(svcs))
	byPort := map[int]string{}
	for i, s := range svcs {
		ports[i] = s.Far()
		byPort[s.Far()] = s.Name
	}

	fmt.Printf("devstack checks %d port(s) on %s@%s\n\n", len(ports), sshUser, host)
	holders, err := tunnel.InspectRemote(sshUser, host, ports)
	if err != nil {
		return err
	}

	held := 0
	for _, h := range holders {
		if h.Info == "" {
			fmt.Printf("  %-24s :%-6d free\n", byPort[h.Port], h.Port)
			continue
		}
		held++
		fmt.Printf("  %-24s :%-6d held by %s\n", byPort[h.Port], h.Port, h.Info)
	}

	fmt.Println()
	if held == 0 {
		fmt.Printf("Every port is free. A push will bind without --reclaim.\n")
		return nil
	}
	fmt.Printf("%d port(s) are held. A push needs --reclaim, which kills those processes.\n", held)
	fmt.Printf("They can belong to a colleague, or to another stack. To narrow the blast radius, pass --service.\n")
	return nil
}
