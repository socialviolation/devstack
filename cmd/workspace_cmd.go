package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Aliases: []string{"ws"},
	Short:   "List and manage workspaces",
	Long: `A workspace is a root directory that holds all the services of one product or
organization. For example, ~/dev/navexa is a workspace. It contains every
microservice and worker of that product.

devstack maintains a global registry of workspaces
(~/.config/devstack/workspaces.json), so it knows where each one is.

One dev daemon serves the whole machine. There is not one daemon for each
workspace. The services of every workspace run in that daemon, with the name
<workspace>:<service>. Each workspace has its own service list in
<workspace>/devstack.workspace.yaml. Each workspace also has its own share of the
machine-wide collector.

The daemon does not run your checkouts. It runs a replica that devstack builds
beside the workspace: one git worktree for each service, at its default branch
tip. Your checkout is the template. 'devstack workspace up' builds the replica,
and it moves each worktree to the current default branch tip.

SUBCOMMANDS
  devstack workspace            show all registered workspaces and service counts
  devstack workspace up         move base to the default branch tip, and run it
  devstack workspace down       stop them, and the daemon if no workspace needs it
  devstack workspace add        register a directory as a workspace
  devstack workspace remove     remove a workspace from the registry
  devstack workspace topology   the service graph: groups, dependencies, dependents
  devstack workspace doctor     examine the manifests and the topology for problems
  devstack workspace generate   rebuild the host daemon's Tiltfile from the manifests
  devstack workspace open       open the dev daemon dashboard in the browser`,
	// Default action: list
	RunE: runWorkspaceList,
}

var workspaceAddCmd = &cobra.Command{
	Use:   "add [path]",
	Short: "Register a directory as a workspace (defaults to current directory)",
	Long: `Register a directory in the global workspace registry. After you register it,
every devstack command that you run in that directory, or in a subdirectory of
it, targets this workspace. You do not need a flag.

devstack then makes the workspace ready to use:
  1. It writes the Claude Code SessionStart hook, so that each session in this
     directory runs 'devstack prime'.
  2. It builds the replica that base runs from: one git worktree for each
     repository of the workspace. A workspace with no service yet gets an empty
     replica, and each later 'devstack init' adds to it.

This command starts nothing. It does not start the daemon, and it does not start
a service.

If you give no path, devstack uses the current working directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWorkspaceAdd,
}

var workspaceRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a workspace from the registry",
	Long:  `devstack removes the workspace entry from the global registry. devstack deletes no files.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceRemove,
}

var workspaceScaffoldServiceCmd = &cobra.Command{
	Use:   "scaffold-service [name]",
	Short: "Write a devstack.service.yaml that teaches, in the current repo or a given one",
	Long: `Write a devstack.service.yaml with full comments. The comments teach how you
declare a service: the run command, the ports, the healthcheck, the env and the
links. devstack writes into the current directory by default. The name defaults
to the directory basename.

'devstack init' supersedes this command. It writes a filled manifest, registers
the repo, and connects MCP and AGENTS in one step.`,
	Hidden:       true,
	Deprecated:   "use `devstack init --name=<n> --path=<p> --cmd=<c>` instead.",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runWorkspaceScaffoldService,
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceAddCmd)
	workspaceCmd.AddCommand(workspaceRemoveCmd)
	workspaceCmd.AddCommand(workspaceScaffoldServiceCmd)

	workspaceAddCmd.Flags().String("name", "", "Workspace name (default: directory basename)")
	workspaceAddCmd.Flags().Int("port", 0, "Dashboard port (default: auto-assign)")
	workspaceAddCmd.Flags().Bool("no-scaffold", false, "Do not create a devstack.workspace.yaml")

	workspaceScaffoldServiceCmd.Flags().String("name", "", "Service name (default: directory basename)")
	workspaceScaffoldServiceCmd.Flags().String("dir", "", "Directory to write into (default: current directory)")
	workspaceScaffoldServiceCmd.Flags().Bool("force", false, "Overwrite an existing devstack.service.yaml")
}

func runWorkspaceScaffoldService(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = cwd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	name, _ := cmd.Flags().GetString("name")
	if name == "" && len(args) > 0 {
		name = args[0]
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	force, _ := cmd.Flags().GetBool("force")

	path, err := scaffoldServiceManifest(abs, name, force)
	if err != nil {
		return err
	}
	fmt.Printf("✓ Wrote %s\n", path)
	fmt.Println("  Fill in runtime.run.command. Then add this repo to the repos list in devstack.workspace.yaml.")
	return nil
}

func runWorkspaceList(cmd *cobra.Command, args []string) error {
	workspaces, err := workspace.All()
	if err != nil {
		return fmt.Errorf("can not load the workspace registry: %w", err)
	}

	if len(workspaces) == 0 {
		fmt.Println("No workspaces registered.")
		fmt.Println("Run 'devstack workspace add' in a workspace directory to register one.")
		return nil
	}

	// Detect current workspace for highlighting
	cwd, _ := os.Getwd()

	fmt.Printf("%-18s %-38s %s\n", "WORKSPACE", "PATH", "SERVICES")
	fmt.Println(strings.Repeat("─", 70))

	for _, ws := range workspaces {
		// Count services from .devstack.json (static, no daemon needed)
		svcCount := ""
		if cfg, err := config.Load(ws.Path); err == nil {
			n := len(cfg.ServicePaths)
			if n == 1 {
				svcCount = "1 service"
			} else {
				svcCount = fmt.Sprintf("%d services", n)
			}
		}

		path := shortDir(ws.Path)
		if len(path) > 36 {
			path = "..." + path[len(path)-33:]
		}

		// Mark active workspace
		marker := "  "
		if cwd == ws.Path || strings.HasPrefix(cwd, ws.Path+"/") {
			marker = "▶ "
		}

		fmt.Printf("%s%-16s %-38s %s\n", marker, ws.Name, path, svcCount)
	}

	return nil
}

func runWorkspaceAdd(cmd *cobra.Command, args []string) error {
	path := ""
	if len(args) > 0 {
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return fmt.Errorf("can not resolve the path: %w", err)
		}
		path = abs
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("can not read the current directory: %w", err)
		}
		path = cwd
	}

	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = filepath.Base(path)
	}
	port, _ := cmd.Flags().GetInt("port")

	ws := workspace.Workspace{
		Name:     name,
		Path:     path,
		TiltPort: port,
	}

	if err := workspace.Register(ws); err != nil {
		return fmt.Errorf("can not register the workspace: %w", err)
	}

	registered, err := workspace.FindByPath(path)
	if err != nil {
		return fmt.Errorf("can not read the registered workspace back: %w", err)
	}

	fmt.Printf("✓ Registered workspace '%s' at %s (dashboard port: %d)\n",
		registered.Name, registered.Path, registered.TiltPort)

	// Bootstrap an educational workspace manifest for fresh workspaces. Leave
	// legacy .devstack.json workspaces alone (migrate deliberately).
	noScaffold, _ := cmd.Flags().GetBool("no-scaffold")
	if !noScaffold {
		if hasLegacyConfig(path) {
			fmt.Println("  Found the legacy .devstack.json. Migrate it to manifests, then run 'devstack workspace generate'.")
		} else {
			wrote, err := scaffoldWorkspaceManifest(path, registered.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  warning: can not write the manifest: %v\n", err)
			} else if wrote {
				fmt.Printf("  ✓ Created %s — add services with 'devstack init --name=<n> --path=<p> --cmd=<c>'.\n", config.WorkspaceManifestFileName)
			}
		}
	}

	prepareWorkspace(registered)
	return nil
}

// prepareWorkspace connects the new workspace to devstack and builds its
// replica, the way 'devstack stack create' prepares each worktree it cuts.
// Whatever creates a thing configures the thing it creates, so a workspace that
// devstack has just registered is ready and never pending.
//
// Neither step is fatal. The directory can hold no manifest yet, and the user
// still has a registered workspace to put one in.
func prepareWorkspace(ws *workspace.Workspace) {
	switch changed, err := ensureClaudeSessionHook(ws.Path); {
	case err != nil:
		fmt.Fprintf(os.Stderr, "  warning: can not write the SessionStart hook: %v\n", err)
	case changed:
		fmt.Printf("  ✓ %s briefs each session with 'devstack prime'\n", filepath.Join(ws.Path, claudeSettingsRel))
	}

	if _, err := ensureReplica(ws); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: can not build the replica: %v\n", err)
		fmt.Fprintln(os.Stderr, "  To build it after you add a service, run: devstack workspace up")
		return
	}
	fmt.Printf("  ✓ Built the replica that base runs from: %s\n", replica.Root(ws))
}

func runWorkspaceRemove(cmd *cobra.Command, args []string) error {
	removed, err := deregisterWorkspace(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("✓ Removed workspace '%s' (%s)\n", removed.Name, removed.Path)
	return nil
}

// deregisterWorkspace drops the registry row for the named workspace (no file
// cleanup) and returns the removed entry. Shared by `workspace remove` and
// `stack rm`.
func deregisterWorkspace(name string) (workspace.Workspace, error) {
	workspaces, err := workspace.Load()
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("can not load the workspace registry: %w", err)
	}

	idx := -1
	for i, ws := range workspaces {
		if strings.EqualFold(ws.Name, name) {
			idx = i
			break
		}
	}
	if idx == -1 {
		return workspace.Workspace{}, fmt.Errorf("there is no workspace %q", name)
	}

	removed := workspaces[idx]
	workspaces = append(workspaces[:idx], workspaces[idx+1:]...)

	if err := workspace.Save(workspaces); err != nil {
		return workspace.Workspace{}, fmt.Errorf("can not save the workspace registry: %w", err)
	}
	return removed, nil
}
