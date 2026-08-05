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
	Short: "Manage the named environments of a workspace",
	Long: `An environment is a named set of configuration values in the workspace manifest.
The values can be database URLs, feature flags or endpoints. devstack defines
every environment once, in the base workspace manifest.

The set subcommand writes values, and it creates the environment when it does not
exist yet. The remove subcommand deletes one. The use subcommand points a scope at
one. The show and which subcommands read them. devstack status shows the
environment that each copy points at.

The use subcommand writes, so it acts on one scope. There are three scopes, and
the most specific one wins. A stack beats a service, and a service beats the
workspace default. Pass --service <svc>, or --stack <name>, or --stack base for
the workspace default. With no flag, the working directory decides the scope. A
stack worktree selects that stack. Every other directory selects the workspace
default.

The set, show, which and remove subcommands need no scope.`,
}

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the environments of the current workspace",
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

	fmt.Printf("\nTo see the values, run: devstack env show <name>\n")
	fmt.Printf("To point a scope at one, run: devstack env use <name> --stack base (or --stack <name>, or --service <svc>)\n")
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
		return fmt.Errorf("devstack can not remove environment %q: %w", envName, err)
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
			return nil, fmt.Errorf("devstack did not find workspace %q: %w", wsName, err)
		}
		return ws, nil
	}
	ws, err := resolveWorkspace("")
	if err != nil {
		return nil, fmt.Errorf("devstack can not detect the workspace from the current directory. Pass --workspace, or set DEVSTACK_WORKSPACE: %w", err)
	}
	return ws, nil
}
