package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hooks"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Lifecycle actions devstack runs when a stack or service changes state",
	Long: `A hook is a shell command devstack runs when a lifecycle event fires: a stack is
created, a service starts, a stack is destroyed. Hooks are declared in
devstack.workspace.yaml (shared with the team, optionally scoped to named
services) and in a service's devstack.service.yaml (that service only).

Because a feature stack's ports are allocated when it is created, a hook cannot
know them in advance. It names them instead: ${self.url}, ${self.port.http} and
${<service>.port.<key>} resolve against the ports that instance actually got.`,
	RunE: runHooksList,
}

var hooksListCmd = &cobra.Command{
	Use:          "list",
	Aliases:      []string{"ls"},
	Short:        "Show declared hooks and which events fire them",
	SilenceUsage: true,
	RunE:         runHooksList,
}

var hooksRunCmd = &cobra.Command{
	Use:   "run <event>",
	Short: "Fire an event's hooks by hand, without performing the lifecycle action",
	Long: `Run the hooks an event would fire, without creating, starting or destroying
anything. This is how you test a hook: the context, port references and error
policy are identical to the real event.

Events: ` + strings.Join(config.HookEvents(), ", "),
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runHooksRun,
}

func init() {
	rootCmd.AddCommand(hooksCmd)
	hooksCmd.AddCommand(hooksListCmd)
	hooksCmd.AddCommand(hooksRunCmd)

	for _, c := range []*cobra.Command{hooksCmd, hooksListCmd, hooksRunCmd} {
		c.Flags().String("stack", "", "Resolve against a feature stack's instances and allocated ports (default: base)")
	}
	hooksListCmd.Flags().String("event", "", "Only show hooks that fire on this event")
	hooksRunCmd.Flags().String("services", "", "Comma-separated service set for the event (default: the stack's overlay, or every service)")
}

// hookContext is the event context and hook sources for one workspace or stack,
// resolved once and shared by every firing point.
type hookContext struct {
	Event  hooks.Event
	Source hooks.Source
}

// buildHookContext resolves everything a hook reads. For a stack it resolves the
// stack's worktree workspace and its allocated ports, so a service hook runs in
// the worktree and ${self.url} names the port that instance is actually on.
// Workspace-level hooks always come from the base manifest: a stack inherits
// them, exactly as it inherits base's environments.
func buildHookContext(ws *workspace.Workspace, stackName string, event string, services []string) (*hookContext, error) {
	baseRW, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace %q: %w", ws.Name, err)
	}

	ev := hooks.Event{
		Name:          event,
		WorkspaceName: ws.Name,
		StackRoot:     ws.Path,
		EnvName:       baseRW.Manifest.Workspace.Env,
	}
	rw := baseRW
	ev.Book = config.BuildPortBook(baseRW)

	if stackName != "" && stackName != "base" {
		rec, err := stack.Resolve(ws.Name, stackName)
		if err != nil {
			return nil, err
		}
		rw, err = stack.ResolveWorktree(rec)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(rw.Services))
		for n := range rw.Services {
			names = append(names, n)
		}
		opts, err := stack.GenerateOptions(rec, names)
		if err != nil {
			return nil, err
		}
		ev.Stack = rec.Name
		ev.StackRoot = rec.Root
		ev.Branch = rec.Branch
		ev.Book = opts.Book
		if rec.Env != "" {
			ev.EnvName = rec.Env
		}
		if len(services) == 0 {
			services = append([]string(nil), rec.Overlay...)
		}
	}

	if len(services) == 0 {
		for n := range rw.Services {
			services = append(services, n)
		}
	}
	sort.Strings(services)
	ev.Services = services

	return &hookContext{Event: ev, Source: hooks.BuildSource(baseRW.Manifest, ws.Path, rw)}, nil
}

// fireHooks runs an event's hooks, writing their output to stderr so a command's
// own stdout stays parseable. A setup event's failure is returned and fails the
// command that fired it; a teardown event's is reported and swallowed, so a
// broken hook can never leave a stack that will not come down.
func fireHooks(ws *workspace.Workspace, stackName, event string, services []string) error {
	ctx, err := buildHookContext(ws, stackName, event, services)
	if err != nil {
		if config.IsTeardownEvent(event) {
			fmt.Fprintf(os.Stderr, "warning: could not resolve hooks for %s: %v\n", event, err)
			return nil
		}
		return fmt.Errorf("could not resolve hooks for %s: %w", event, err)
	}
	_, err = hooks.Run(ctx.Event, ctx.Source, os.Stderr)
	return err
}

func runHooksList(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	stackName, _ := cmd.Flags().GetString("stack")
	eventFilter, _ := cmd.Flags().GetString("event")
	if eventFilter != "" && !isHookEvent(eventFilter) {
		return fmt.Errorf("unknown event %q; known events: %s", eventFilter, strings.Join(config.HookEvents(), ", "))
	}

	events := config.HookEvents()
	if eventFilter != "" {
		events = []string{eventFilter}
	}

	target := "base"
	if stackName != "" && stackName != "base" {
		target = "stack " + stackName
	}
	fmt.Printf("Hooks for workspace %s (%s)\n\n", ws.Name, target)

	total := 0
	for _, event := range events {
		ctx, err := buildHookContext(ws, stackName, event, nil)
		if err != nil {
			return err
		}
		invocations := hooks.Resolve(ctx.Event, ctx.Source)
		if len(invocations) == 0 {
			continue
		}
		total += len(invocations)
		fmt.Printf("%s\n", event)
		for _, inv := range invocations {
			fmt.Printf("  %-28s %-32s %s\n", inv.Label(), inv.Origin,
				fmt.Sprintf("onError=%s timeout=%s", inv.Hook.ResolvedOnError(event), inv.Hook.ResolvedTimeout()))
			color.New(color.Faint).Printf("    %s\n", inv.Hook.Run)
		}
		fmt.Println()
	}

	if total == 0 {
		fmt.Printf("No hooks declared. Add a 'hooks:' block to %s, or to a service's %s.\n",
			config.WorkspaceManifestFileName, config.ServiceManifestFileName)
		fmt.Printf("Events: %s\n", strings.Join(config.HookEvents(), ", "))
		return nil
	}
	color.New(color.Faint).Println("A hook labelled name/service runs once for that service, with it as ${self}.")
	color.New(color.Faint).Println("Fire one by hand without doing the lifecycle action: devstack hooks run <event>")
	return nil
}

func runHooksRun(cmd *cobra.Command, args []string) error {
	event := args[0]
	if !isHookEvent(event) {
		return fmt.Errorf("unknown event %q; known events: %s", event, strings.Join(config.HookEvents(), ", "))
	}

	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	stackName, _ := cmd.Flags().GetString("stack")
	servicesFlag, _ := cmd.Flags().GetString("services")

	ctx, err := buildHookContext(ws, stackName, event, splitCSV(servicesFlag))
	if err != nil {
		return err
	}
	invocations := hooks.Resolve(ctx.Event, ctx.Source)
	if len(invocations) == 0 {
		fmt.Printf("No hooks fire on %s for %s. See: devstack hooks list\n", event, ctx.Event.StackLabel())
		return nil
	}

	fmt.Printf("Firing %s for %s (services: %s)\n", event, ctx.Event.StackLabel(), strings.Join(ctx.Event.Services, ", "))
	results, runErr := hooks.Run(ctx.Event, ctx.Source, os.Stdout)
	failed := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}
	fmt.Printf("\n%d hook(s) ran, %d failed.\n", len(results), failed)
	return runErr
}

func isHookEvent(name string) bool {
	for _, e := range config.HookEvents() {
		if e == name {
			return true
		}
	}
	return false
}
