package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

var stackCmd = &cobra.Command{
	Use:   "stack",
	Short: "Create and manage feature stacks that overlay a base workspace",
	Long: `A feature stack runs a subset of a base workspace's services from their own
git worktrees, reusing the base stack for everything else. Only the services you
change (and the services that call them) get a worktree and a dynamically
allocated port; the rest resolve to the base stack.`,
	RunE: runStackList,
}

var stackCreateCmd = &cobra.Command{
	Use:          "create <name> --repos a,b",
	Short:        "Create a feature stack overlaying the base workspace",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackCreate,
}

var stackRemoveCmd = &cobra.Command{
	Use:          "rm <name>",
	Aliases:      []string{"remove"},
	Short:        "Stop a stack, remove its worktrees, release its ports, and deregister it",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runStackRemove,
}

var stackListCmd = &cobra.Command{
	Use:          "list",
	Aliases:      []string{"ls"},
	Short:        "List registered feature stacks",
	SilenceUsage: true,
	RunE:         runStackList,
}

func init() {
	rootCmd.AddCommand(stackCmd)
	stackCmd.AddCommand(stackCreateCmd)
	stackCmd.AddCommand(stackRemoveCmd)
	stackCmd.AddCommand(stackListCmd)

	stackCreateCmd.Flags().String("repos", "", "Comma-separated service names that this stack changes")
	stackRemoveCmd.Flags().Bool("force", false, "Remove worktrees even if they have uncommitted changes")
}

// qualifyPortKey encodes a (service, portKey) pair into the single allocation key
// persisted by AllocatePorts, so stack-aware generation can rebuild the overlay
// PortBook from the flat map that LoadPorts returns.
func qualifyPortKey(service, key string) string {
	return service + "/" + key
}

// splitPortKey reverses qualifyPortKey. ok is false for a malformed key.
func splitPortKey(qualified string) (service, key string, ok bool) {
	i := strings.Index(qualified, "/")
	if i <= 0 || i == len(qualified)-1 {
		return "", "", false
	}
	return qualified[:i], qualified[i+1:], true
}

// stackGenerateOptions builds the tiltgen options for a feature stack: the
// overlay-first merged PortBook (base's pinned ports with the stack's allocated
// ports layered over its overlay services) and the OTEL export env pointed at the
// base's collector, since a stack never runs its own.
func stackGenerateOptions(ws *workspace.Workspace, names []string) (tiltgen.Options, error) {
	base, err := workspace.FindByName(ws.BaseName)
	if err != nil {
		return tiltgen.Options{}, fmt.Errorf("stack %q base workspace %q not found in registry: %w", ws.Name, ws.BaseName, err)
	}
	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return tiltgen.Options{}, fmt.Errorf("failed to resolve base workspace %q at %s: %w", base.Name, base.Path, err)
	}

	allocated, err := workspace.LoadPorts(ws.Name)
	if err != nil {
		return tiltgen.Options{}, err
	}
	overlay := config.PortBook{}
	for qualified, port := range allocated {
		service, key, ok := splitPortKey(qualified)
		if !ok {
			continue
		}
		if overlay[service] == nil {
			overlay[service] = map[string]int{}
		}
		overlay[service][key] = port
	}

	return tiltgen.Options{
		ManagedEnv: workspace.ManagedEnv(base, names),
		Book:       config.MergeStackBook(config.BuildPortBook(baseRW), overlay),
	}, nil
}

func runStackCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	reposFlag, _ := cmd.Flags().GetString("repos")
	changed := splitCSV(reposFlag)
	if len(changed) == 0 {
		return fmt.Errorf("--repos is required: name the service(s) this stack changes")
	}

	base, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	if base.IsStack() {
		return fmt.Errorf("%q is itself a stack; create a stack from a base workspace", base.Name)
	}

	baseRW, err := config.ResolveWorkspace(base.Path)
	if err != nil {
		return fmt.Errorf("failed to resolve base workspace: %w", err)
	}
	topo, err := config.BuildTopology(base.Path)
	if err != nil {
		return err
	}
	for _, s := range changed {
		if _, ok := topo.Services[s]; !ok {
			return fmt.Errorf("unknown service %q in workspace %q; known services: %s",
				s, base.Name, strings.Join(topo.ServiceNames(), ", "))
		}
	}

	overlay, err := config.OverlaySet(topo, changed)
	if err != nil {
		return err
	}
	changedSet := stringSet(changed)

	fmt.Printf("Base workspace: %s (%s)\n", base.Name, base.Path)
	fmt.Printf("Overlay set (changed ∪ transitive callers):\n")
	for _, s := range overlay {
		if changedSet[s] {
			fmt.Printf("  %-16s changed\n", s)
		} else {
			fmt.Printf("  %-16s pulled in (calls a changed service)\n", s)
		}
	}

	parent := filepath.Dir(base.Path)
	stackRoot := filepath.Join(parent, ".devstack-stacks", name)
	if stackRoot == base.Path || strings.HasPrefix(stackRoot, base.Path+string(os.PathSeparator)) {
		return fmt.Errorf("refusing: stack root %s would be nested under base %s (breaks workspace detection)", stackRoot, base.Path)
	}
	worktreePaths := map[string]string{}
	for _, s := range overlay {
		worktreePaths[s] = filepath.Join(stackRoot, s)
	}
	if err := os.MkdirAll(stackRoot, 0755); err != nil {
		return fmt.Errorf("failed to create stack root %s: %w", stackRoot, err)
	}
	fmt.Printf("Stack root (sibling of base): %s\n", stackRoot)

	var dirty []string
	for _, s := range overlay {
		repoPath := baseRW.Services[s].RepoPath
		res, err := worktree.Create(repoPath, worktreePaths[s], name, changedSet[s])
		if err != nil {
			return fmt.Errorf("worktree for %q: %w", s, err)
		}
		if res.SourceDirty {
			dirty = append(dirty, s)
		}
		branchNote := "detached at HEAD"
		if changedSet[s] {
			branchNote = "branch " + name
		}
		fmt.Printf("  ✓ worktree %-16s %s (%s)\n", s, worktreePaths[s], branchNote)
	}
	if len(dirty) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: uncommitted changes left in the base checkout — worktrees hold committed HEAD only: %s\n", strings.Join(dirty, ", "))
	}

	stackName := base.Name + "--" + name

	manifest, err := config.GenerateStackManifest(baseRW, stackName, overlay, func(s string) string { return worktreePaths[s] })
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal stack manifest: %w", err)
	}
	manifestPath := config.WorkspaceManifestPath(stackRoot)
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write stack manifest: %w", err)
	}
	fmt.Printf("  ✓ generated %s\n", manifestPath)

	if err := workspace.Register(workspace.Workspace{Name: stackName, Path: stackRoot, BaseName: base.Name}); err != nil {
		return fmt.Errorf("failed to register stack: %w", err)
	}
	reg, err := workspace.FindByName(stackName)
	if err != nil {
		return err
	}
	fmt.Printf("  ✓ registered %q (base %q, daemon port %d)\n", reg.Name, reg.BaseName, reg.TiltPort)

	var keys []string
	for _, s := range overlay {
		svc := baseRW.Services[s]
		if svc.Manifest == nil {
			continue
		}
		portKeys := make([]string, 0, len(svc.Manifest.Ports))
		for k := range svc.Manifest.Ports {
			portKeys = append(portKeys, k)
		}
		sort.Strings(portKeys)
		for _, k := range portKeys {
			keys = append(keys, qualifyPortKey(s, k))
		}
	}
	allocated := map[string]int{}
	if len(keys) > 0 {
		allocated, err = workspace.AllocatePorts(stackName, keys)
		if err != nil {
			return fmt.Errorf("failed to allocate service ports: %w", err)
		}
	}
	fmt.Printf("Allocated service ports (key scheme: service/portKey):\n")
	for _, k := range sortedKeys(allocated) {
		fmt.Printf("  %-24s http://localhost:%d\n", k, allocated[k])
	}

	if !isTiltReachable(fmt.Sprintf("http://localhost:%d/api/view", base.TiltPort)) {
		fmt.Fprintf(os.Stderr, "WARNING: base %q daemon is not reachable on port %d. A stack reuses base's running services — start base first: (cd %s && devstack up)\n",
			base.Name, base.TiltPort, base.Path)
	}

	fmt.Printf("\nStack %q ready. Start it: (cd %s && devstack up)\n", stackName, stackRoot)
	return nil
}

func runStackRemove(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")
	ws, err := resolveStack(args[0])
	if err != nil {
		return err
	}

	fmt.Printf("Removing stack %q (base %q)\n", ws.Name, ws.BaseName)

	if err := stopStackDaemon(ws); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not stop stack daemon: %v\n", err)
	}

	if rw, err := config.ResolveWorkspace(ws.Path); err == nil {
		svcNames := make([]string, 0, len(rw.Services))
		for n := range rw.Services {
			svcNames = append(svcNames, n)
		}
		sort.Strings(svcNames)
		for _, n := range svcNames {
			p := rw.Services[n].RepoPath
			if err := worktree.Remove(p, force); err != nil {
				return fmt.Errorf("remove worktree %s: %w\n(use --force to discard uncommitted work)", p, err)
			}
			fmt.Printf("  ✓ removed worktree %s\n", p)
		}
	} else {
		fmt.Fprintf(os.Stderr, "warning: could not resolve stack manifest to list worktrees: %v\n", err)
	}

	if err := workspace.ReleasePorts(ws.Name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to release ports: %v\n", err)
	} else {
		fmt.Printf("  ✓ released allocated ports\n")
	}

	if _, err := deregisterWorkspace(ws.Name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to deregister: %v\n", err)
	} else {
		fmt.Printf("  ✓ deregistered %q\n", ws.Name)
	}

	if err := os.RemoveAll(workspace.DataDir(ws.Name)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to remove data dir: %v\n", err)
	}
	if err := os.RemoveAll(ws.Path); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to remove stack root: %v\n", err)
	} else {
		fmt.Printf("  ✓ removed stack root %s\n", ws.Path)
	}

	fmt.Printf("✓ Stack %q removed.\n", ws.Name)
	return nil
}

func runStackList(cmd *cobra.Command, args []string) error {
	all, err := workspace.All()
	if err != nil {
		return fmt.Errorf("failed to load workspace registry: %w", err)
	}

	var stacks []workspace.Workspace
	for _, ws := range all {
		if ws.IsStack() {
			stacks = append(stacks, ws)
		}
	}
	if len(stacks) == 0 {
		fmt.Println("No stacks registered. Create one with: devstack stack create <name> --repos <svc>")
		return nil
	}

	fmt.Printf("%-24s %-14s %-6s %-9s %s\n", "STACK", "BASE", "PORT", "STATUS", "LINKS")
	fmt.Println(strings.Repeat("-", 90))
	for _, ws := range stacks {
		ports, _ := workspace.LoadPorts(ws.Name)
		links := make([]string, 0, len(ports))
		for _, k := range sortedKeys(ports) {
			links = append(links, fmt.Sprintf("%s=http://localhost:%d", k, ports[k]))
		}
		linkStr := "-"
		if len(links) > 0 {
			linkStr = strings.Join(links, " ")
		}
		fmt.Printf("%-24s %-14s %-6d %-9s %s\n", ws.Name, ws.BaseName, ws.TiltPort, stackStatus(ws), linkStr)
	}
	return nil
}

// resolveStack finds a registered stack by its full name (base--feature) or by
// its short feature name when that is unambiguous across registered stacks.
func resolveStack(name string) (*workspace.Workspace, error) {
	all, err := workspace.All()
	if err != nil {
		return nil, err
	}
	var matches []workspace.Workspace
	for _, ws := range all {
		if !ws.IsStack() {
			continue
		}
		if strings.EqualFold(ws.Name, name) || strings.HasSuffix(strings.ToLower(ws.Name), "--"+strings.ToLower(name)) {
			matches = append(matches, ws)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("stack %q not found", name)
	case 1:
		w := matches[0]
		return &w, nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return nil, fmt.Errorf("stack %q is ambiguous; use the full name: %s", name, strings.Join(names, ", "))
	}
}

// stopStackDaemon stops a stack's dev daemon: disable its services, kill the
// process, remove the PID file, and close the session. A stack has no infra or
// collector of its own, so this is the whole teardown for its daemon.
func stopStackDaemon(ws *workspace.Workspace) error {
	pidFile := workspace.PIDFile(ws.Name)
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return fmt.Errorf("invalid PID in %s: %w", pidFile, err)
	}

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	if view, err := tiltClient.GetView(); err == nil {
		for _, r := range view.UiResources {
			if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
				continue
			}
			tiltClient.RunCLI("disable", r.Metadata.Name) //nolint:errcheck
		}
	}
	if proc, err := os.FindProcess(pid); err == nil {
		proc.Kill()
	}
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: failed to remove PID file: %v\n", err)
	}

	ports := []int{ws.TiltPort}
	if session, err := workspace.LoadSession(ws.Name); err == nil && len(session.ActivePorts) > 0 {
		ports = session.ActivePorts
	}
	residue := workspace.DetectResidue(pid, ports)
	if err := workspace.CloseSession(ws.Name, residue); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to close session: %v\n", err)
	}
	fmt.Printf("  ✓ stopped daemon (pid %d)\n", pid)
	return nil
}

func stackStatus(ws workspace.Workspace) string {
	if isTiltReachable(fmt.Sprintf("http://localhost:%d/api/view", ws.TiltPort)) {
		return "running"
	}
	if data, err := os.ReadFile(workspace.PIDFile(ws.Name)); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && isProcessAlive(pid) {
			return "starting"
		}
	}
	return "stopped"
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, i := range items {
		set[i] = true
	}
	return set
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
