package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
)

var envSetCmd = &cobra.Command{
	Use:   "set <name> KEY=VALUE [KEY=VALUE ...]",
	Short: "Set config-var values on a named environment",
	Long:  "Set config-var values on one of the base workspace's named environments.\nEnvironments are defined once in the base workspace manifest and inherited by feature stacks.",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runEnvSet,
}

var envUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Point a scope (workspace/service/stack) at a named environment",
	Long:  "Point a scope at one of the base workspace's named environments.\nEnvironments are defined once in the base workspace manifest; feature stacks don't define their own. --stack points a stack at one of the base's environments (likewise --service for a service, or no flag for the workspace). <name> must be defined in the base workspace.",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnvUse,
}

var envShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a named environment's config-var values",
	Long:  "Show a base workspace environment's config-var values (secrets masked).\nEnvironments are defined once in the base workspace manifest.",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnvShow,
}

var envWhichCmd = &cobra.Command{
	Use:   "which",
	Short: "Show the active env at each scope and the merged effective values",
	Long:  "Show which base-defined environment a service resolves to at each scope (workspace/service/stack) and the merged effective values (stack > service > workspace, secrets masked).",
	Args:  cobra.NoArgs,
	RunE:  runEnvWhich,
}

func init() {
	envCmd.AddCommand(envSetCmd, envUseCmd, envShowCmd, envWhichCmd)

	envUseCmd.Flags().String("service", "", "apply at the service scope")
	envUseCmd.Flags().String("stack", "", "apply at the stack scope")

	envWhichCmd.Flags().String("service", "", "service to resolve (defaults to the current directory)")
	envWhichCmd.Flags().String("stack", "", "stack whose env to include")
}

func runEnvSet(cmd *cobra.Command, args []string) error {
	name := args[0]
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	for _, pair := range args[1:] {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid KEY=VALUE pair %q", pair)
		}
		if err := config.SetEnvValue(ws.Path, name, key, value); err != nil {
			return err
		}
		fmt.Printf("set %s.%s = %s\n", name, key, mask(key, value))
	}
	return nil
}

func runEnvUse(cmd *cobra.Command, args []string) error {
	name := args[0]
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	m, err := config.LoadWorkspaceManifest(ws.Path)
	if err != nil {
		return err
	}
	if _, ok := m.Environments[name]; !ok {
		return fmt.Errorf("env %q is not defined in workspace %q; available: %s", name, ws.Name, envNames(m))
	}

	stackName, _ := cmd.Flags().GetString("stack")
	svcName, _ := cmd.Flags().GetString("service")
	switch {
	case stackName != "":
		rec, err := stack.Resolve(ws.Name, stackName)
		if err != nil {
			return err
		}
		if err := stack.SetEnv(ws.Name, rec.Name, name); err != nil {
			return err
		}
		fmt.Printf("stack %q now uses env %q\n", rec.Name, name)
	case svcName != "":
		rw, err := config.ResolveWorkspace(ws.Path)
		if err != nil {
			return err
		}
		svc, ok := rw.Services[svcName]
		if !ok {
			return fmt.Errorf("service %q not found in workspace %q; services: %s", svcName, ws.Name, strings.Join(sortedServiceNames(rw), ", "))
		}
		if err := config.SetServiceEnv(svc.RepoPath, name); err != nil {
			return err
		}
		fmt.Printf("service %q now uses env %q\n", svcName, name)
	default:
		if err := config.SetWorkspaceEnv(ws.Path, name); err != nil {
			return err
		}
		fmt.Printf("workspace %q now uses env %q\n", ws.Name, name)
	}
	return nil
}

func runEnvShow(cmd *cobra.Command, args []string) error {
	name := args[0]
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	m, err := config.LoadWorkspaceManifest(ws.Path)
	if err != nil {
		return err
	}
	env, ok := m.Environments[name]
	if !ok {
		return fmt.Errorf("env %q is not defined in workspace %q; available: %s", name, ws.Name, envNames(m))
	}

	fmt.Printf("Environment %q values (secrets masked):\n\n", name)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "KEY\tVALUE")
	fmt.Fprintln(w, "---\t-----")
	for _, k := range sortedStrKeys(env.Values) {
		fmt.Fprintf(w, "%s\t%s\n", k, mask(k, env.Values[k]))
	}
	w.Flush()
	return nil
}

func runEnvWhich(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return err
	}

	svcName, _ := cmd.Flags().GetString("service")
	if svcName == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		identity, err := config.ResolveIdentity(cwd)
		if err != nil {
			return fmt.Errorf("could not detect a service from the current directory; pass --service: %w", err)
		}
		svcName = identity.ServiceName
	}
	if svcName == "" {
		return fmt.Errorf("no service resolved; pass --service <svc>")
	}
	svc, ok := rw.Services[svcName]
	if !ok {
		return fmt.Errorf("service %q not found in workspace %q; services: %s", svcName, ws.Name, strings.Join(sortedServiceNames(rw), ", "))
	}

	stackEnv := ""
	if stackName, _ := cmd.Flags().GetString("stack"); stackName != "" {
		rec, err := stack.Resolve(ws.Name, stackName)
		if err != nil {
			return err
		}
		stackEnv = rec.Env
	}

	merged, err := config.ResolveEnvPatch(rw.Manifest, svc.Manifest, stackEnv)
	if err != nil {
		return err
	}

	fmt.Printf("Active env by scope for service %q:\n\n", svcName)
	fmt.Printf("  workspace.env  %s\n", orDash(rw.Manifest.Workspace.Env))
	fmt.Printf("  service.env    %s\n", orDash(svc.Manifest.Service.Env))
	fmt.Printf("  stack.env      %s\n\n", orDash(stackEnv))

	fmt.Printf("Merged effective values (stack > service > workspace, secrets masked):\n\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "KEY\tVALUE")
	fmt.Fprintln(w, "---\t-----")
	for _, k := range sortedStrKeys(merged) {
		fmt.Fprintf(w, "%s\t%s\n", k, mask(k, merged[k]))
	}
	w.Flush()
	return nil
}

func mask(key, value string) string {
	if svcconfig.IsSecret(key, value) {
		return svcconfig.MaskedValue
	}
	return value
}

func orDash(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func envNames(m *config.WorkspaceManifest) string {
	names := make([]string, 0, len(m.Environments))
	for k := range m.Environments {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

func sortedStrKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
