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
	Long: `devstack is a service manager for local development. It is built for a team that
works across many services and repositories. It is the backbone of an AI-assisted
local development workflow.

WHAT IT DOES
  devstack manages the services that run on your machine: APIs, workers,
  importers and more. It organizes them into workspaces, one workspace for each
  product or organization. It starts the services in dependency order, shows
  their live state, and restarts them.

  devstack runs one OpenTelemetry collector (OpenObserve) for the whole machine.
  Every workspace shares it. Each service sends its traces and logs there, and an
  AI agent can query them as they arrive. When a feature breaks, the agent calls
  the MCP tools, reads the correlated traces and logs, and finds the cause
  without leaving the editor.

WORKSPACE AUTO-DETECTION
  Run any command inside a workspace directory, or inside any service
  subdirectory. devstack detects the workspace for you. No flag is necessary.

TYPICAL WORKFLOW
  devstack workspace add              register this directory as a workspace
  devstack workspace up               start the daemon, and build the replica base runs from
  devstack init --name=api ...        register a service and connect observability
  devstack service start <service> --stack base
                                      start a service and all its dependencies
  devstack status                     live grouped view of every service
  devstack otel traces                query traces from the configured backend
  devstack otel open                  open the trace UI in the browser

AI AGENT WORKFLOW
  devstack serve                      expose MCP tools to the AI agent
  devstack init --all                 connect every service to the MCP server
  devstack prime                      brief this session on what runs here`,
}

func Execute() {
	// Cobra prints the error itself; printing it here as well put every message
	// on stderr twice, once prefixed "Error:" and once bare.
	rootCmd.SilenceErrors = true
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Hide the built-in help subcommand (--help flag still works)
	rootCmd.SetHelpCommand(&cobra.Command{Hidden: true})

	rootCmd.Version = versionLine()
	rootCmd.SetVersionTemplate("devstack {{.Version}}\n")

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "Configuration file (default: ./config.json)")
	_ = rootCmd.PersistentFlags().MarkHidden("config")

	// Dev daemon connection. There is no port knob: one daemon serves the whole
	// machine on a fixed port, and every command reaches it there.
	rootCmd.PersistentFlags().String("daemon-host", "localhost", "Dev daemon host")
	_ = rootCmd.PersistentFlags().MarkHidden("daemon-host")

	// Default service context
	rootCmd.PersistentFlags().String("default-service", "", "Default service name, used when a command names none (env: DEVSTACK_DEFAULT_SERVICE)")

	// Workspace root directory
	rootCmd.PersistentFlags().String("workspace", "", "Workspace name or path. Default: the workspace of the current directory (env: DEVSTACK_WORKSPACE)")

	// Bind flags to viper (keep internal keys stable)
	viper.BindPFlag("tilt.host", rootCmd.PersistentFlags().Lookup("daemon-host"))
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

	// Environment variable bindings (new devstack names, with legacy TILT_* fallback)
	viper.BindEnv("tilt.host", "DEVSTACK_DAEMON_HOST", "TILT_HOST")
	viper.BindEnv("default_service", "DEVSTACK_DEFAULT_SERVICE")
	viper.BindEnv("workspace", "DEVSTACK_WORKSPACE")

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using configuration file:", viper.ConfigFileUsed())
	}
}
