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
	Long: `A hook is a shell command that devstack runs when a lifecycle event fires. A
stack is created, a service starts, a stack is destroyed. You declare hooks in
devstack.workspace.yaml, which you share with the team and can scope to named
services. You can also declare them in a service manifest, which covers that
service only. A service manifest is devstack.service.yaml, or
devstack.<name>.yaml for each service after the first one in that directory.

devstack allocates a feature stack's ports when it creates the stack, so a hook
can not know them in advance. A hook names them instead. The references
${self.url}, ${self.port.http} and ${<service>.port.<key>} resolve against the
ports that the copy got.

A hook that fails on a setup event stops the hooks behind it, and it fails the
command. A hook that fails on a teardown event is reported and skipped, and the
teardown carries on.

A failed stack.destroy hook is the one failure that you can not retry. devstack
removes the stack, and that deletes the record that ${self...} resolves against.
devstack prints the resolved URLs at the point of failure. Clean up by hand from
those.`,
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
	Short: "Fire an event's hooks by hand, without the lifecycle action",
	Long: `Run the hooks that an event fires. This command creates nothing, it starts
nothing and it destroys nothing. This is how you test a hook. The context, the
port references and the error policy are the same as for the real event.

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
		c.Flags().String("stack", "", "Resolve against a feature stack's copies and allocated ports. Default: base")
	}
	hooksListCmd.Flags().String("event", "", "Only show the hooks that fire on this event")
	hooksRunCmd.Flags().String("services", "", "Comma-separated services for the event. Default: the stack's overlay, or every service")
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
		return fmt.Errorf("unknown event %q. Known events: %s", eventFilter, strings.Join(config.HookEvents(), ", "))
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
	color.New(color.Faint).Println("A hook labeled name/service runs once for that service, with it as ${self}.")
	color.New(color.Faint).Println("To fire one by hand, without the lifecycle action, run: devstack hooks run <event>")
	return nil
}

func runHooksRun(cmd *cobra.Command, args []string) error {
	event := args[0]
	if !isHookEvent(event) {
		return fmt.Errorf("unknown event %q. Known events: %s", event, strings.Join(config.HookEvents(), ", "))
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
		fmt.Printf("No hooks fire on %s for %s. To see them all, run: devstack hooks list\n", event, ev.StackLabel())
		return nil
	}

	fmt.Printf("devstack fires %s for %s (services: %s)\n", event, ev.StackLabel(), strings.Join(ev.Services, ", "))
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
