package cmd

import "github.com/spf13/cobra"

var tiltCmd = &cobra.Command{
	Use:   "tilt",
	Short: "Manage the Tilt daemon and services",
}

func init() {
	rootCmd.AddCommand(tiltCmd)
}
