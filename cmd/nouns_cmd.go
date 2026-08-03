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
	Long: `Act on one service. The name must be a service: to act on a group, use
'devstack group <action> <name>'.

With no name, devstack works out the service from the working directory.

These actions change what is running, so they also need the copy to act on:
--stack <name> for a feature stack, or --stack base for base — which runs from
the replica devstack keeps, not from your checkout. There is no default. With no
flag devstack uses the copy whose directory you are standing in, and refuses in
a plain checkout rather than acting on code you are not looking at.`,
}

var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Start, stop and restart a named set of services, and manage the sets",
	Long: `A group is a named set of services that you operate on together. Starting a
group starts every service in it, in dependency order.

The name must be a group: to act on one service, use 'devstack service <action> <name>'.

start, stop and restart change what is running, so they need the copy to act on
too: --stack <name>, or --stack base. There is no default.`,
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
	serviceRestartCmd = serviceAction("restart [service]", "Restart a service, without restarting what it depends on", runRestart, cobra.MaximumNArgs(1))

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
		c.Flags().String("stack", "", "Which copy to act on: a feature stack's name, or \"base\" for the replica base runs from. Required unless the working directory is inside one of them")
	}
}
