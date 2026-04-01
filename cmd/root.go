package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "devstack",
	Short: "Run and observe local development services across one or more workspaces",
	Long: `devstack is a local development service manager built for teams working across
multiple services and repositories. It is the backbone of an AI-assisted local
development workflow.

WHAT IT DOES
  devstack manages groups of locally running services (APIs, workers, importers,
  etc.) organised into workspaces — one workspace per product or organisation.
  It handles dependency-ordered startup, live status, and service restarts.

  It also spins up a local OpenTelemetry observability stack (SigNoz) per
  workspace, so every service ships traces and logs that AI agents can query
  in real time. When something breaks during feature development, an AI agent
  can call the MCP tools to pull correlated traces and logs and pinpoint the
  root cause without leaving the editor.

WORKSPACE AUTO-DETECTION
  Run any command from inside a workspace directory or any service subdirectory.
  devstack will detect which workspace you are in automatically — no flags needed.

TYPICAL WORKFLOW
  devstack workspace add              register this directory as a workspace
  devstack workspace up               start the dev daemon
  devstack init --name=api ...        register a service and wire up observability
  devstack start <service>            start a service and all its dependencies
  devstack status                     live grouped view of every service
  devstack otel open                  open the SigNoz trace UI in the browser

AI AGENT WORKFLOW
  devstack serve                      expose MCP tools to the AI agent
  devstack init --all                 write AGENTS.md instructions into every service`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Hide the built-in help subcommand (--help flag still works)
	rootCmd.SetHelpCommand(&cobra.Command{Hidden: true})

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./config.json)")
	_ = rootCmd.PersistentFlags().MarkHidden("config")

	// Dashboard (internal dev daemon) connection
	rootCmd.PersistentFlags().Int("dashboard-port", 10350, "Dashboard port")
	rootCmd.PersistentFlags().String("dashboard-host", "localhost", "Dashboard host")
	_ = rootCmd.PersistentFlags().MarkHidden("dashboard-host")

	// Default service context
	rootCmd.PersistentFlags().String("default-service", "", "Default service name when none is specified (env: DEVSTACK_DEFAULT_SERVICE)")

	// Workspace root directory
	rootCmd.PersistentFlags().String("workspace", "", "Workspace name or path (env: DEVSTACK_WORKSPACE)")

	// Bind flags to viper (keep internal keys stable)
	viper.BindPFlag("tilt.port", rootCmd.PersistentFlags().Lookup("dashboard-port"))
	viper.BindPFlag("tilt.host", rootCmd.PersistentFlags().Lookup("dashboard-host"))
	viper.BindPFlag("default_service", rootCmd.PersistentFlags().Lookup("default-service"))
	viper.BindPFlag("workspace", rootCmd.PersistentFlags().Lookup("workspace"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("json")
		viper.AddConfigPath(".")
		viper.AddConfigPath("$HOME/.devstack")
	}

	// Environment variable bindings
	viper.BindEnv("tilt.port", "TILT_PORT")
	viper.BindEnv("tilt.host", "TILT_HOST")
	viper.BindEnv("default_service", "DEVSTACK_DEFAULT_SERVICE")
	viper.BindEnv("workspace", "DEVSTACK_WORKSPACE")

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}
