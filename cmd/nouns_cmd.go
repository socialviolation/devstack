package cmd

import (
	"github.com/spf13/cobra"
)

// The command surface is noun first: `devstack <noun> <action> [target]`. One
// name can be both a service and a group — navexa has a group "roi" and a
// service aliased "roi" — and a verb-first `devstack service stop roi` had to guess
// which you meant, silently. The noun says it.
//
// Each action reuses the run function the verb-first form used, and fixes the
// target kind through an annotation, so `group stop` cannot act on a service and
// `service stop` cannot act on a group.

var serviceCmd = &cobra.Command{
	Use:     "service",
	Aliases: []string{"svc"},
	Short:   "Start, stop and restart the services of this workspace",
	Long: `Act on one service. The name must be a service. To act on a group, run
'devstack group <action> <name>'.

With no name, devstack finds the service from the working directory.

These actions change what runs, so they also name the copy to act on. Pass
--stack <name> for a feature stack, or --stack base for base. Base runs from the
replica that devstack keeps, not from your checkout.

With no flag, devstack acts on the copy whose directory you are in. Anywhere
else it acts on base. Each action names the copy it changed: a base copy has no
:stack suffix.`,
}

var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Start, stop and restart a named set of services, and manage the sets",
	Long: `A group is a named set of services that you act on together. When you start a
group, devstack starts every service in it, in dependency order.

The name must be a group. To act on one service, run 'devstack service <action> <name>'.

start, stop and restart change what runs, so they also name the copy to act on:
--stack <name>, or --stack base. With no flag they use the copy whose directory
you are in, and base anywhere else.

A stack rarely overlays a whole group. With --stack <name>, the action reaches
only the members that the stack runs its own copy of. devstack names the members
that still serve from base. If no member of the group is in the stack, devstack
refuses. It does not narrow the group without a word to you. To act on the
members that stay on base, run the same command with --stack base.`,
	RunE: runGroupsList,
}

func serviceAction(use, short string, run func(*cobra.Command, []string) error, args cobra.PositionalArgs) *cobra.Command {
	return &cobra.Command{
		Use:          use,
		Short:        short,
		Args:         args,
		SilenceUsage: true,
		RunE:         run,
		Annotations:  map[string]string{"targetKind": string(targetService)},
	}
}

func groupAction(use, short string, run func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:          use,
		Short:        short,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE:         run,
		Annotations:  map[string]string{"targetKind": string(targetGroup)},
	}
}

var (
	serviceStartCmd   = serviceAction("start [service]", "Start a service and all the services it depends on", runEnable, cobra.MaximumNArgs(1))
	serviceStopCmd    = serviceAction("stop [service]", "Stop a service", runStop, cobra.MaximumNArgs(1))
	serviceRestartCmd = serviceAction("restart [service]", "Restart a service, but not the services it depends on", runRestart, cobra.MaximumNArgs(1))

	groupStartCmd   = groupAction("start <group>", "Start every service in a group, in dependency order", runEnable)
	groupStopCmd    = groupAction("stop <group>", "Stop every service in a group", runStop)
	groupRestartCmd = groupAction("restart <group>", "Restart every service in a group", runRestart)
)

func init() {
	rootCmd.AddCommand(serviceCmd)
	rootCmd.AddCommand(groupCmd)

	serviceCmd.AddCommand(serviceStartCmd, serviceStopCmd, serviceRestartCmd)
	groupCmd.AddCommand(groupStartCmd, groupStopCmd, groupRestartCmd)
	groupCmd.AddCommand(groupsListCmd, groupsAddCmd, groupsRemoveCmd)

	for _, c := range []*cobra.Command{
		serviceStartCmd, serviceStopCmd, serviceRestartCmd,
		groupStartCmd, groupStopCmd, groupRestartCmd,
	} {
		c.Flags().String("stack", "", "The copy to act on: a feature stack name, or \"base\" for the replica that base runs from. Default: the copy your directory is in, or base")
	}
}
