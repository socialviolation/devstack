package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
)

// resolveExplainContext resolves the active workspace + environment from flags,
// env vars, and cwd. Shared by topology and workspace doctor.
func resolveExplainContext(cmd *cobra.Command) (*config.ResolvedContext, error) {
	workspaceValue, _ := cmd.Root().PersistentFlags().GetString("workspace")
	envValue, _ := cmd.Root().PersistentFlags().GetString("env")

	workspaceFlagChanged := cmd.Root().PersistentFlags().Lookup("workspace").Changed
	envFlagChanged := cmd.Root().PersistentFlags().Lookup("env").Changed

	invocationEnv := map[string]string{}
	if value := os.Getenv("DEVSTACK_WORKSPACE"); value != "" {
		invocationEnv["DEVSTACK_WORKSPACE"] = value
	}
	if value := os.Getenv("DEVSTACK_ENVIRONMENT"); value != "" {
		invocationEnv["DEVSTACK_ENVIRONMENT"] = value
	}

	opts := config.ResolveOptions{
		InvocationEnv: invocationEnv,
	}
	if workspaceFlagChanged {
		opts.WorkspacePath = workspaceValue
	}
	if envFlagChanged {
		opts.EnvironmentName = envValue
	}
	return config.ResolveContext(opts)
}

// printConfigExplain prints the active workspace + environment resolution.
func printConfigExplain(ctx *config.ResolvedContext) {
	fmt.Printf("Workspace: %s\n", ctx.WorkspaceName.Value)
	printValue("root", ctx.WorkspaceRoot)
	printValue("environment", ctx.EnvironmentName)
	if ctx.CurrentService.Value != "" {
		printValue("current service", ctx.CurrentService)
	}
	fmt.Printf("services: %d\n", len(ctx.Workspace.Services))
}

// printServiceExplain prints the resolved configuration for one service and the
// source of each value.
func printServiceExplain(ctx *config.ResolvedContext, serviceName string) error {
	service, err := config.ResolveServiceConfig(ctx, serviceName)
	if err != nil {
		return err
	}
	fmt.Printf("Service: %s\n", service.Name)
	printValue("workspace", ctx.WorkspaceName)
	printValue("path", service.Path)
	printValue("run command", service.RunCommand)
	printValue("work dir", service.WorkDir)
	printValue("healthcheck type", service.HealthcheckType)
	printValue("healthcheck url", service.HealthcheckURL)
	printList("groups", service.Groups)
	printList("dependencies", service.Dependencies)
	printList("env files", service.EnvFiles)
	printValue("telemetry service name", service.TelemetryService)
	printValue("traces expected", service.TracesExpected)
	printValue("logs expected", service.LogsExpected)
	return nil
}

func printValue(label string, value config.SourcedValue) {
	line := fmt.Sprintf("%s: %s", label, printableValue(value.Value))
	var extras []string
	if value.Source != "" {
		extras = append(extras, fmt.Sprintf("source=%s", value.Source))
	}
	if value.Detail != "" {
		extras = append(extras, fmt.Sprintf("detail=%s", value.Detail))
	}
	if value.Path != "" {
		extras = append(extras, fmt.Sprintf("path=%s", value.Path))
	}
	if len(extras) > 0 {
		line += fmt.Sprintf(" [%s]", strings.Join(extras, ", "))
	}
	fmt.Println(line)
}

func printList(label string, values []config.SourcedValue) {
	if len(values) == 0 {
		fmt.Printf("%s: -\n", label)
		return
	}
	for i, value := range values {
		itemLabel := label
		if i > 0 {
			itemLabel = ""
		}
		printValue(itemLabel, value)
	}
}

func printableValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
