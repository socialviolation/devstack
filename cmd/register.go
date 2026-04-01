package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"devstack/internal/workspace"
)

var registerCmd = &cobra.Command{
	Use:    "register",
	Short:  "Register a workspace in the global registry",
	Long:   `Adds or updates a workspace entry in ~/.config/devstack/workspaces.json.`,
	Hidden: true,
	RunE:   runRegister,
}

func init() {
	rootCmd.AddCommand(registerCmd)
	registerCmd.Flags().String("name", "", "Workspace name (default: basename of --path or cwd)")
	registerCmd.Flags().String("path", "", "Workspace root directory (default: current working directory)")
	registerCmd.Flags().Int("port", 0, "Tilt API port (default: auto-assigned)")
}

func runRegister(cmd *cobra.Command, args []string) error {
	path, _ := cmd.Flags().GetString("path")
	name, _ := cmd.Flags().GetString("name")
	port, _ := cmd.Flags().GetInt("port")

	// Resolve path
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		path = cwd
	} else {
		abs, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("failed to resolve path: %w", err)
		}
		path = abs
	}

	// Resolve name
	if name == "" {
		name = filepath.Base(path)
	}

	ws := workspace.Workspace{
		Name:     name,
		Path:     path,
		TiltPort: port,
	}

	if err := workspace.Register(ws); err != nil {
		return fmt.Errorf("failed to register workspace: %w", err)
	}

	// Read back to get the actual port (may have been auto-assigned)
	registered, err := workspace.FindByPath(path)
	if err != nil {
		return fmt.Errorf("failed to read back registered workspace: %w", err)
	}

	fmt.Printf("✓ Registered workspace '%s' at %s (Tilt port: %d)\n", registered.Name, registered.Path, registered.TiltPort)
	return nil
}
