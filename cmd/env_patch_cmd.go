package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
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
	Long:  "Show a base workspace environment's config-var values. Credentials are redacted in place — a connection string still shows its server and database. Pass --reveal to print them in the clear.\nEnvironments are defined once in the base workspace manifest.",
	Args:  cobra.ExactArgs(1),
	RunE:  runEnvShow,
}

var envWhichCmd = &cobra.Command{
	Use:   "which",
	Short: "Show the env a service actually resolves to, and where each value came from",
	Long:  "Show which base-defined environment a service resolves to at each scope (workspace/service/stack), then the full resolved environment the process receives — every key with the ladder rung it came from (.envrc, env.files, manifest env.values, active env, devstack-computed). Credentials are redacted in place — a connection string still shows its server and database. Pass --reveal to print them in the clear.",
	Args:  cobra.NoArgs,
	RunE:  runEnvWhich,
}

func init() {
	envCmd.AddCommand(envSetCmd, envUseCmd, envShowCmd, envWhichCmd)

	envUseCmd.Flags().String("service", "", "apply at the service scope")
	envUseCmd.Flags().String("stack", "", "apply at the stack scope")

	envWhichCmd.Flags().String("service", "", "service to resolve (defaults to the current directory)")
	envWhichCmd.Flags().String("stack", "", "stack whose env to include")
	envWhichCmd.Flags().Bool("shadowed", false, "also list the lower-rung values each key overrode")

	envShowCmd.Flags().Bool("reveal", false, "print every value in full, including credentials, in the clear")
	envWhichCmd.Flags().Bool("reveal", false, "print every value in full, including credentials, in the clear")
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
		fmt.Printf("set %s.%s = %s\n", name, key, mask(key, value, false))
	}
	if _, err := regenerateHostTiltfile(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not regenerate host config: %v\n", err)
	} else {
		fmt.Println("Regenerated host config. Restart the affected service to apply: devstack restart <service> [--stack <name>]")
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
	var restartHint string
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
		restartHint = fmt.Sprintf("devstack restart <svc> --stack %s", rec.Name)
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
		restartHint = fmt.Sprintf("devstack restart %s", svcName)
	default:
		if err := config.SetWorkspaceEnv(ws.Path, name); err != nil {
			return err
		}
		fmt.Printf("workspace %q now uses env %q\n", ws.Name, name)
		restartHint = "devstack restart <service>"
	}
	if _, err := regenerateHostTiltfile(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not regenerate host config: %v\n", err)
	} else {
		fmt.Printf("Regenerated host config. Restart to apply: %s\n", restartHint)
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
		return unknownEnvError(name, ws.Name, m)
	}
	reveal, _ := cmd.Flags().GetBool("reveal")

	fmt.Printf("Environment %q:\n", name)

	fmt.Printf("\nConfig-var values (%s):\n\n", redactionNote(reveal))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "KEY\tVALUE")
	fmt.Fprintln(w, "---\t-----")
	for _, k := range sortedStrKeys(env.Values) {
		fmt.Fprintf(w, "%s\t%s\n", k, mask(k, env.Values[k], reveal))
	}
	w.Flush()
	if len(env.Values) == 0 {
		fmt.Println("(none)")
	}

	fmt.Printf("\nAlso in scope for services here, but NOT part of this env:\n\n")
	ow := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(ow, "workspace env.values\t%s\n", orNoneDefined(sortedStrKeys(m.Env.Values)))
	fmt.Fprintf(ow, "workspace env.files\t%s\n", orNoneDefined(m.Env.Files))
	fmt.Fprintf(ow, "devstack-computed\t%s\n", strings.Join(workspace.ManagedEnvKeys(), ", "))
	fmt.Fprintf(ow, "service env.values, .envrc\tper service — see env which\n")
	ow.Flush()

	color.New(color.Faint).Printf("\nEverything a service actually receives, each value with its source:\n  devstack env which --service <svc> [--stack <name>]\n")
	return nil
}

// orNoneDefined renders a key list, making an empty one explicit rather than
// silent — absence and omission must not look the same.
func orNoneDefined(keys []string) string {
	if len(keys) == 0 {
		return "(none defined)"
	}
	return strings.Join(keys, ", ")
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

	stackName, _ := cmd.Flags().GetString("stack")
	var rec *stack.Record
	if stackName != "" {
		rec, err = stack.Resolve(ws.Name, stackName)
		if err != nil {
			return err
		}
	}

	var srw *config.ResolvedWorkspace
	if rec != nil {
		srw, err = stack.ResolveWorktree(rec)
		if err != nil {
			return err
		}
	}

	svcName, _ := cmd.Flags().GetString("service")
	if svcName == "" && srw != nil {
		if len(srw.Services) != 1 {
			return fmt.Errorf("stack %q has %d services; pass --service: %s", rec.Name, len(srw.Services), strings.Join(sortedServiceNames(srw), ", "))
		}
		for k := range srw.Services {
			svcName = k
		}
	}
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
	target := rw
	if srw != nil {
		if _, ok := srw.Services[svcName]; ok {
			target = srw
		}
	}
	svc, ok := target.Services[svcName]
	if !ok {
		return fmt.Errorf("service %q not found in workspace %q; services: %s", svcName, ws.Name, strings.Join(sortedServiceNames(rw), ", "))
	}

	stackEnv := ""
	scope := "base (no stack)"
	if rec != nil {
		stackEnv = rec.Env
		scope = "stack " + rec.FullName()
	}

	fmt.Printf("Active env by scope for service %q:\n\n", svcName)
	fmt.Printf("  workspace.env  %s\n", orDash(target.Manifest.Workspace.Env))
	fmt.Printf("  service.env    %s\n", orDash(svc.Manifest.Service.Env))
	fmt.Printf("  stack.env      %s\n", orDash(stackEnv))

	layers, err := resolvedEnvLadder(ws, target, svc, rec)
	if err != nil {
		return err
	}
	shadowed, _ := cmd.Flags().GetBool("shadowed")
	reveal, _ := cmd.Flags().GetBool("reveal")
	rows := buildEnvRows(layers, reveal)

	fmt.Printf("\nResolved environment for %s in %s — what the process receives (%s):\n\n", svcName, scope, redactionNote(reveal))
	printEnvRows(rows, shadowed)
	if !shadowed && anyShadowed(rows) {
		fmt.Printf("\nSome keys are set at more than one rung; see them with --shadowed.\n")
	}
	return nil
}

// resolvedEnvLadder builds the full env precedence ladder a service's process
// receives, with ${service.field} refs resolved against the port book the same
// way the generated serve_env resolves them. rec is nil for a base service.
func resolvedEnvLadder(ws *workspace.Workspace, rw *config.ResolvedWorkspace, svc config.ResolvedService, rec *stack.Record) ([]config.EnvLayer, error) {
	names := sortedServiceNames(rw)

	var managed map[string]string
	var book config.PortBook
	stackEnv := ""
	if rec != nil {
		stackEnv = rec.Env
		if opts, err := stack.GenerateOptions(rec, names); err == nil {
			managed = opts.ManagedEnv[svc.Name]
			book = opts.Book
		}
	} else {
		managed = workspace.ManagedEnv(ws, names)[svc.Name]
		book = config.BuildPortBook(rw)
	}

	layers, err := config.EnvLadder(svc.EnvDir(), rw.Manifest, svc.Manifest, stackEnv, managed)
	if err != nil {
		return nil, err
	}
	if book != nil {
		if err := tiltgen.ResolveLayerRefs(layers, svc.Name, book); err != nil {
			return nil, err
		}
	}
	return layers, nil
}

// envRow is one key of the resolved environment: the value that wins, the layer
// it came from, and the lower-rung values it buried.
type envRow struct {
	Key      string
	Value    string
	Rung     config.EnvRung
	Source   string
	Shadowed []envShadow
}

type envShadow struct {
	Rung   config.EnvRung
	Source string
	Value  string
	By     string
}

// buildEnvRows attributes every key of a merged ladder to the highest layer that
// defines it, lowest rung first. Shadowed carries the buried layers in ladder
// order; By names the layer that immediately buried one, when that is not the
// winning layer itself.
func buildEnvRows(layers []config.EnvLayer, reveal bool) []envRow {
	winner := map[string]int{}
	for i, l := range layers {
		for k := range l.Values {
			winner[k] = i
		}
	}
	keys := make([]string, 0, len(winner))
	for k := range winner {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]envRow, 0, len(keys))
	for _, k := range keys {
		top := layers[winner[k]]
		row := envRow{Key: k, Value: mask(k, top.Values[k], reveal), Rung: top.Rung, Source: envSourceLabel(top)}
		for i := 0; i < winner[k]; i++ {
			v, ok := layers[i].Values[k]
			if !ok {
				continue
			}
			sh := envShadow{Rung: layers[i].Rung, Source: envSourceLabel(layers[i]), Value: mask(k, v, reveal)}
			if j, ok := nextDefining(layers, i, k); ok && j != winner[k] {
				sh.By = envSourceLabel(layers[j])
			}
			row.Shadowed = append(row.Shadowed, sh)
		}
		rows = append(rows, row)
	}
	return rows
}

func nextDefining(layers []config.EnvLayer, after int, key string) (int, bool) {
	for i := after + 1; i < len(layers); i++ {
		if _, ok := layers[i].Values[key]; ok {
			return i, true
		}
	}
	return 0, false
}

// envSourceLabel names a layer in plain text. Colour is decoration only: this
// string alone must tell the reader where a value came from.
func envSourceLabel(l config.EnvLayer) string {
	switch l.Rung {
	case config.RungActiveEnv:
		if l.Source == "" {
			return string(config.RungActiveEnv)
		}
		return fmt.Sprintf("active env (%s)", l.Source)
	case config.RungWorkspaceFiles, config.RungServiceFiles:
		if l.Source == "" {
			return string(l.Rung)
		}
		return fmt.Sprintf("%s (%s)", l.Rung, l.Source)
	default:
		return string(l.Rung)
	}
}

func rungColor(r config.EnvRung) *color.Color {
	switch r {
	case config.RungManaged:
		return color.New(color.FgMagenta)
	case config.RungActiveEnv:
		return color.New(color.FgCyan)
	case config.RungServiceValues:
		return color.New(color.FgGreen)
	case config.RungWorkspaceValues:
		return color.New(color.FgYellow)
	case config.RungServiceFiles:
		return color.New(color.FgBlue)
	case config.RungWorkspaceFiles:
		return color.New(color.FgHiBlue)
	default:
		return color.New(color.Faint)
	}
}

// pad right-pads to width in printable runes, so masked values (•) and other
// multi-byte text still line up.
func pad(s string, width int) string {
	if n := width - utf8.RuneCountInString(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func anyShadowed(rows []envRow) bool {
	for _, r := range rows {
		if len(r.Shadowed) > 0 {
			return true
		}
	}
	return false
}

func printEnvRows(rows []envRow, shadowed bool) {
	keyW, valW := len("KEY"), len("VALUE")
	for _, r := range rows {
		keyW = max(keyW, utf8.RuneCountInString(r.Key))
		valW = max(valW, utf8.RuneCountInString(r.Value))
	}
	keyW = min(keyW, 48)
	valW = min(valW, 56)

	fmt.Printf("  %s   %s   %s\n", pad("KEY", keyW), pad("VALUE", valW), "SOURCE")
	fmt.Printf("  %s   %s   %s\n", pad("---", keyW), pad("-----", valW), "------")
	for _, r := range rows {
		fmt.Printf("  %s   %s   %s\n", pad(r.Key, keyW), pad(r.Value, valW), rungColor(r.Rung).Sprint(r.Source))
		if !shadowed {
			continue
		}
		for i := len(r.Shadowed) - 1; i >= 0; i-- {
			s := r.Shadowed[i]
			by := ""
			if s.By != "" {
				by = " (buried by " + s.By + ")"
			}
			fmt.Printf("    ↳ overridden: %s = %s%s\n", rungColor(s.Rung).Sprint(s.Source), s.Value, by)
		}
	}
}

// unknownEnvError explains "base"/"default" — which users type because --stack
// base is accepted elsewhere — rather than listing envs they did not ask for.
func unknownEnvError(name, wsName string, m *config.WorkspaceManifest) error {
	if name == "base" || name == "default" {
		return fmt.Errorf("%q is not an environment name — it means \"no stack\", the un-stacked instance of a service.\nWorkspace %q defines these environments: %s\nTo see what a base service actually resolves to, run: devstack env which --service <svc>", name, wsName, envNames(m))
	}
	return fmt.Errorf("env %q is not defined in workspace %q; available: %s", name, wsName, envNames(m))
}

func mask(key, value string, reveal bool) string {
	if reveal {
		return value
	}
	return svcconfig.RedactValue(key, value)
}

func redactionNote(reveal bool) string {
	if reveal {
		return "REVEALED — secrets printed in the clear"
	}
	return "credentials redacted"
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
