package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage devstack environments",
	Long: `Named environments for your workspace, defined in the workspace manifest.

An environment is a named config-var patch (DB URLs, feature flags, endpoints).
Define them with set (which creates the env on demand) and drop them with remove;
apply them with use; inspect with show/which. status shows where each instance
points.

use applies at one of three scopes, the most specific winning: stack beats
service, which beats the workspace default. It changes what services run with, so
it names its scope — --service <svc>, --stack <name>, or --stack base for the
workspace default — and has no default. set, show, which and remove need no
scope.`,
}

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List environments for the current workspace",
	RunE:  runEnvList,
}

var envRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a named environment",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnvRemove,
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.AddCommand(envListCmd, envRemoveCmd)
}

func runEnvList(cmd *cobra.Command, args []string) error {
	ws, err := resolveEnvWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	m, err := config.LoadWorkspaceManifest(ws.Path)
	if err != nil {
		return err
	}

	usage := envUsage{
		WorkspaceEnv: m.Workspace.Env,
		ServiceEnvs:  map[string]string{},
		StackEnvs:    map[string]string{},
	}
	if rw, err := config.ResolveWorkspace(ws.Path); err == nil {
		for name, svc := range rw.Services {
			if svc.Manifest != nil {
				usage.ServiceEnvs[name] = svc.Manifest.Service.Env
			}
		}
	}
	if recs, err := stack.LoadStore(ws.Name); err == nil {
		for _, r := range recs {
			usage.StackEnvs[r.Name] = r.Env
		}
	}
	applied := appliedTo(usage)

	names := make([]string, 0, len(m.Environments))
	for k := range m.Environments {
		names = append(names, k)
	}
	sort.Strings(names)

	nameW, appliedW := len("NAME"), len("APPLIED TO")
	for _, name := range names {
		nameW = max(nameW, len(name))
		appliedW = max(appliedW, len(appliedLabel(applied[name])))
	}

	fmt.Printf("Environments for workspace %q:\n\n", ws.Name)
	fmt.Printf("  %-*s   %-*s   %s\n", nameW, "NAME", appliedW, "APPLIED TO", "KEYS")
	fmt.Printf("  %-*s   %-*s   %s\n", nameW, "----", appliedW, "----------", "----")
	for _, name := range names {
		env := m.Environments[name]
		scopes := applied[name]
		label := fmt.Sprintf("%-*s", appliedW, appliedLabel(scopes))
		if len(scopes) == 0 {
			label = color.New(color.Faint).Sprint(label)
		} else {
			label = color.New(color.FgCyan).Sprint(label)
		}
		fmt.Printf("  %-*s   %s   %s\n", nameW, name, label, formatEnvKeys(env.Values, 6))
		if d := strings.TrimSpace(env.Description); d != "" {
			color.New(color.Faint).Printf("  %-*s   %s\n", nameW, "", d)
		}
	}

	fmt.Printf("\nValues: devstack env show <name>\n")
	fmt.Printf("To switch: devstack env use <name> --stack base (or --stack <name>, or --service <svc>)\n")
	return nil
}

// envUsage is every place an environment name can be selected from.
type envUsage struct {
	WorkspaceEnv string
	ServiceEnvs  map[string]string
	StackEnvs    map[string]string
}

// appliedTo maps each selected env name to the scopes that select it, in plain
// text: the workspace, each service, and each stack.
func appliedTo(u envUsage) map[string][]string {
	out := map[string][]string{}
	if u.WorkspaceEnv != "" {
		out[u.WorkspaceEnv] = append(out[u.WorkspaceEnv], "workspace")
	}
	for _, name := range sortedStrKeys(u.ServiceEnvs) {
		if env := u.ServiceEnvs[name]; env != "" {
			out[env] = append(out[env], "service: "+name)
		}
	}
	for _, name := range sortedStrKeys(u.StackEnvs) {
		if env := u.StackEnvs[name]; env != "" {
			out[env] = append(out[env], "stack: "+name)
		}
	}
	return out
}

func appliedLabel(scopes []string) string {
	if len(scopes) == 0 {
		return "unused"
	}
	return strings.Join(scopes, ", ")
}

// formatEnvKeys lists an environment's config-var key names — never values, which
// can be secrets — truncating to limit with a count of the remainder.
func formatEnvKeys(values map[string]string, limit int) string {
	keys := sortedStrKeys(values)
	if len(keys) == 0 {
		return "(none)"
	}
	if len(keys) <= limit {
		return strings.Join(keys, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(keys[:limit], ", "), len(keys)-limit)
}

func runEnvRemove(cmd *cobra.Command, args []string) error {
	envName := args[0]

	ws, err := resolveEnvWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}

	if err := config.RemoveEnvironment(ws.Path, envName); err != nil {
		return fmt.Errorf("failed to remove environment: %w", err)
	}

	fmt.Printf("Removed environment %q from workspace %q\n", envName, ws.Name)
	return nil
}

// resolveEnvWorkspace finds the workspace to operate on for env commands.
// Uses --workspace flag, DEVSTACK_WORKSPACE env var, or detects from cwd.
func resolveEnvWorkspace(wsName string) (*workspace.Workspace, error) {
	if wsName != "" {
		ws, err := workspace.FindByName(wsName)
		if err != nil {
			return nil, fmt.Errorf("workspace %q not found: %w", wsName, err)
		}
		return ws, nil
	}
	ws, err := resolveWorkspace("")
	if err != nil {
		return nil, fmt.Errorf("can not detect workspace from current directory. Use --workspace or DEVSTACK_WORKSPACE: %w", err)
	}
	return ws, nil
}
