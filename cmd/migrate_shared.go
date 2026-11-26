package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/migrate"
)

var migrateSharedStacksCmd = &cobra.Command{
	Use:   "shared-stacks",
	Short: "Convert a workspace to the shared set and small stacks. Without --apply, change nothing",
	Long: `Convert one workspace to the shared set and small stacks.

Without --apply, devstack prints what it changes, and it changes nothing.

THE STEPS
  shared    If devstack.workspace.yaml has no shared: list, devstack proposes
            one. It proposes each service that starts a container, and each
            store. devstack writes the list only from --shared. You confirm the
            list.
  edges     devstack moves the workspace maps dependencies, startsAfter and
            calls onto each service, as calls in its devstack.service.yaml.
            Then it removes the maps from devstack.workspace.yaml. --needs
            makes an edge a needs edge.
  groups    devstack removes the shared services from each group.
  stacks    devstack removes the shared services from the members of each
            stack. It writes each record in the current schema, and it writes
            each stack manifest again. It releases the ports of the shared
            services in each stack. It removes no worktree, record or branch.
  refs      devstack names each ${svc.*} reference to a stack service. It
            changes nothing for these.

BEFORE IT WRITES
  It copies stacks.json and each file it edits to
  <data dir>/migrate-backup-<time>/, and it prints that path.

Run this command again at any time. A second run changes nothing.

  devstack migrate shared-stacks
  devstack migrate shared-stacks --apply --shared navexa-sql,azurite
  devstack migrate shared-stacks --apply --needs navexa-api=navexa-sql`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runMigrateSharedStacks,
}

func init() {
	migrateCmd.AddCommand(migrateSharedStacksCmd)
	migrateSharedStacksCmd.Flags().Bool("apply", false, "Write the changes. Without it, devstack changes nothing")
	migrateSharedStacksCmd.Flags().String("shared", "", "The shared services, comma separated. devstack writes this list to shared: with --apply")
	migrateSharedStacksCmd.Flags().StringSlice("needs", nil, "Make an edge a needs edge, as svc=dep. Give it more than once, or comma separated")
}

func runMigrateSharedStacks(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	opts := migrate.SharedStacksOptions{}
	opts.Apply, _ = cmd.Flags().GetBool("apply")
	if cmd.Flags().Changed("shared") {
		raw, _ := cmd.Flags().GetString("shared")
		opts.Shared = migrate.SplitNames(raw)
	}
	pairs, _ := cmd.Flags().GetStringSlice("needs")
	if opts.Needs, err = migrate.ParseNeeds(pairs); err != nil {
		return err
	}
	return migrate.SharedStacks(os.Stdout, ws, opts)
}
