package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/hooks"
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
${<service>.port.<key>} resolve against the ports that instance got.`,
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

// fireHooks runs an event's hooks, writing their output to stderr so a command's
// own stdout stays parseable.
func fireHooks(ws *workspace.Workspace, stackName, event string, services []string) error {
	return hooks.Fire(ws, stackName, event, services, os.Stderr)
}

// fireTeardownHooks runs a teardown event's hooks and never stops the caller.
//
// A teardown action always proceeds. A hook that sets onError "abort" stops the
// hooks that follow it, and it does not stop the removal — a stack you cannot
// remove because a hook is broken leaks a worktree, a port and a record every
// time. The failure is reported, because it means the external cleanup probably
// did not happen.
func fireTeardownHooks(ws *workspace.Workspace, stackName, event string, services []string) {
	if err := fireHooks(ws, stackName, event, services); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		fmt.Fprintf(os.Stderr, "warning: the teardown continues. Clean up outside this machine by hand.\n")
	}
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
		ev, src, err := hooks.Context(ws, stackName, event, nil)
		if err != nil {
			return err
		}
		invocations := hooks.Resolve(ev, src)
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

	ev, src, err := hooks.Context(ws, stackName, event, splitCSV(servicesFlag))
	if err != nil {
		return err
	}
	invocations := hooks.Resolve(ev, src)
	if len(invocations) == 0 {
		fmt.Printf("No hooks fire on %s for %s. See: devstack hooks list\n", event, ev.StackLabel())
		return nil
	}

	fmt.Printf("Firing %s for %s (services: %s)\n", event, ev.StackLabel(), strings.Join(ev.Services, ", "))
	results, runErr := hooks.Run(ev, src, os.Stdout)
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
