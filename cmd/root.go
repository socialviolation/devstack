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
	Short: "Navexa dev stack MCP server",
	Long: `An MCP (Model Context Protocol) server that bridges Tilt's dev stack
interface to Claude Code, enabling AI assistants to manage the Navexa dev stack.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./config.json)")

	// Tilt configuration
	rootCmd.PersistentFlags().Int("tilt-port", 10350, "Tilt API port")
	rootCmd.PersistentFlags().String("tilt-host", "localhost", "Tilt API host")

	// Default service context
	rootCmd.PersistentFlags().String("default-service", "", "Default service name when none is specified (env: NVXDEV_DEFAULT_SERVICE)")

	// Workspace root directory
	rootCmd.PersistentFlags().String("workspace", "", "Root directory containing all projects managed by this devstack (env: DEVSTACK_WORKSPACE)")

	// Bind flags to viper
	viper.BindPFlag("tilt.port", rootCmd.PersistentFlags().Lookup("tilt-port"))
	viper.BindPFlag("tilt.host", rootCmd.PersistentFlags().Lookup("tilt-host"))
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
	viper.BindEnv("default_service", "NVXDEV_DEFAULT_SERVICE")
	viper.BindEnv("workspace", "DEVSTACK_WORKSPACE")

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}
