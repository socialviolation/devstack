package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/workspace"
)

var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Aliases: []string{"ws"},
	Short:   "List and manage workspaces",
	Long: `A workspace is a root directory that groups all the services for a single
product or organisation. For example, ~/dev/navexa is a workspace containing
every microservice and worker that makes up that product.

devstack maintains a global registry of workspaces (~/.config/devstack/workspaces.json)
so it knows where each one lives and which port its dev daemon is running on.

Each workspace has its own:
  - dev daemon   (the background process that runs and hot-reloads services)
  - service list (declared in <workspace>/.devstack.json)
  - observability stack (SigNoz, started automatically with 'workspace up')

SUBCOMMANDS
  devstack workspace            show all registered workspaces and service counts
  devstack workspace up         start the dev daemon for the current workspace
  devstack workspace down       stop the dev daemon
  devstack workspace add        register a directory as a workspace
  devstack workspace remove     remove a workspace from the registry`,
	// Default action: list
	RunE: runWorkspaceList,
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show all registered workspaces and their service counts",
	Args:  cobra.NoArgs,
	RunE:  runWorkspaceList,
}

var workspaceAddCmd = &cobra.Command{
	Use:   "add [path]",
	Short: "Register a directory as a workspace (defaults to current directory)",
	Long: `Register a directory in the global workspace registry. Once registered,
any devstack command run inside the directory (or its subdirectories) will
automatically target this workspace without needing any flags.

If no path is given, the current working directory is used.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWorkspaceAdd,
}

var workspaceRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a workspace from the registry",
	Long:  `Removes the workspace entry from the global registry. Does not delete any files.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceRemove,
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceAddCmd)
	workspaceCmd.AddCommand(workspaceRemoveCmd)

	workspaceAddCmd.Flags().String("name", "", "Workspace name (default: directory basename)")
	workspaceAddCmd.Flags().Int("port", 0, "Dashboard port (default: auto-assign)")
}

func runWorkspaceList(cmd *cobra.Command, args []string) error {
	workspaces, err := workspace.All()
	if err != nil {
		return fmt.Errorf("failed to load workspace registry: %w", err)
	}

	if len(workspaces) == 0 {
		fmt.Println("No workspaces registered.")
		fmt.Println("Run 'devstack workspace add' from a workspace directory to register one.")
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
			return fmt.Errorf("failed to resolve path: %w", err)
		}
		path = abs
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
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
		return fmt.Errorf("failed to register workspace: %w", err)
	}

	registered, err := workspace.FindByPath(path)
	if err != nil {
		return fmt.Errorf("failed to read back registered workspace: %w", err)
	}

	fmt.Printf("✓ Registered workspace '%s' at %s (dashboard port: %d)\n",
		registered.Name, registered.Path, registered.TiltPort)
	return nil
}

func runWorkspaceRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	workspaces, err := workspace.Load()
	if err != nil {
		return fmt.Errorf("failed to load workspace registry: %w", err)
	}

	idx := -1
	for i, ws := range workspaces {
		if strings.EqualFold(ws.Name, name) {
			idx = i
			break
		}
	}

	if idx == -1 {
		return fmt.Errorf("workspace %q not found", name)
	}

	removed := workspaces[idx]
	workspaces = append(workspaces[:idx], workspaces[idx+1:]...)

	if err := workspace.Save(workspaces); err != nil {
		return fmt.Errorf("failed to save workspace registry: %w", err)
	}

	fmt.Printf("✓ Removed workspace '%s' (%s)\n", removed.Name, removed.Path)
	return nil
}
