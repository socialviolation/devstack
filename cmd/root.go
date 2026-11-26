package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/stack"
)

// exitMigrateRefused is the status of a migration that stopped before it wrote.
// 'devstack upgrade' runs the migration in another process, so the exit status
// is the only thing it reads. A refusal leaves every replica and every running
// copy as they were, and a write that failed does not, so the two need different
// statuses.
const exitMigrateRefused = 3

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "devstack",
	Short: "Run and observe local development services across one or more workspaces",
	Long: `devstack runs the services of this machine for local development.

A workspace is one product: every service of that product, in one directory. A
stack is a group of those services that you cut for one feature. A stack is
local infrastructure that does not last. You stand it up, and you tear it down.
An environment points a stack at a database and at a set of endpoints.

The shared services run once for the workspace, and every stack uses them.
'devstack workspace up' keeps them on the default branch, so you do not
maintain them.

Run any command in a workspace directory, or in a service directory. devstack
detects the workspace for you. No flag is necessary.`,
}

func Execute() {
	installHelp()
	// Cobra prints the error itself; printing it here as well put every message
	// on stderr twice, once prefixed "Error:" and once bare.
	rootCmd.SilenceErrors = true
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		if errors.Is(err, migrate.ErrRefused) {
			os.Exit(exitMigrateRefused)
		}
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if cmd != tickCmd && cmd != stackSweepCmd && cmd != stackGCCmd {
			sweepIfDirect(time.Now())
		}
	}

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
	viper.BindEnv("stop_after", "DEVSTACK_STOP_AFTER")
	viper.BindEnv("agents.max_stacks", "DEVSTACK_AGENT_MAX_STACKS")
	viper.BindEnv("agents.ttl", "DEVSTACK_AGENT_TTL")

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using configuration file:", viper.ConfigFileUsed())
	}

	stack.StopAfter = stopAfter(viper.GetViper(), os.Stderr)
}

func stopAfter(v *viper.Viper, warn io.Writer) time.Duration {
	if v.IsSet("agents.max_stacks") {
		fmt.Fprintln(warn, "Warning: DEVSTACK_AGENT_MAX_STACKS and agents.max_stacks are deprecated. devstack has no agent quota, and ignores them.")
	}
	raw := v.GetString("stop_after")
	if raw == "" && v.IsSet("agents.ttl") {
		fmt.Fprintln(warn, "Warning: DEVSTACK_AGENT_TTL and agents.ttl are deprecated. Use DEVSTACK_STOP_AFTER or stop_after.")
		raw = v.GetString("agents.ttl")
	}
	if raw == "" {
		return defaultStopAfter
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		fmt.Fprintf(warn, "Warning: the stop window %q is not a positive duration, such as 24h. devstack uses %s.\n", raw, defaultStopAfter)
		return defaultStopAfter
	}
	return d
}

const defaultStopAfter = 24 * time.Hour
