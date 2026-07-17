package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Register a service and write AI agent instructions",
	Long: `Register a service into the workspace and write AI agent instructions to AGENTS.md.

FIRST-TIME SERVICE SETUP (provide --name, --path, --cmd)
  Registers a new service so devstack can run and observe it:
    1. Writes devstack.service.yaml in the service repo (how devstack runs it)
    2. Adds the repo to the workspace manifest's repoDiscovery.repos list
    3. Creates .mcp.json to wire up the devstack MCP server for AI agents
    4. Writes AGENTS.md with instructions for AI agents on how to run and observe it
    5. Regenerates the dev daemon config so 'devstack start' can run it

  Use --force to overwrite an existing service manifest (e.g. to update the run command).

REFRESH ONLY (no --name/--path/--cmd flags)
  Re-writes the devstack section of AGENTS.md in the current service directory with
  the latest instructions. Use --all to refresh every service in the workspace at once.

LANGUAGE AUTO-DETECTION
  devstack inspects --path for known files:
    *.csproj          → dotnet
    go.mod            → go
    requirements.txt  → python
    package.json      → node
  Override with --language.

EXAMPLES
  devstack init --name=api --path=/dev/myorg/api --cmd="go run ."
  devstack init --name=api --path=/dev/myorg/api --cmd="go run ." --force
  devstack init                    # refresh AGENTS.md in current directory
  devstack init --all              # refresh AGENTS.md in every service`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().String("name", "", "Service name")
	initCmd.Flags().String("path", "", "Absolute path to the service directory")
	initCmd.Flags().String("cmd", "", "Command to run the service (e.g. \"go run .\" or \"dotnet run\")")
	initCmd.Flags().Int("port", 0, "HTTP port the service listens on (enables health checks and dashboard links)")
	initCmd.Flags().String("language", "", "Language override: dotnet, python, node, go (default: auto-detect)")
	initCmd.Flags().String("group", "", "Suggest a group for the service (add it with 'devstack groups add')")
	initCmd.Flags().Bool("all", false, "Refresh AGENTS.md for every registered service in the workspace")
	initCmd.Flags().Bool("force", false, "Overwrite existing service configuration if it already exists")
}

func runInit(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	name, _ := cmd.Flags().GetString("name")

	// --all: refresh AGENTS.md for every service
	if all {
		return runInitAll()
	}

	// No --name: refresh mode (AGENTS.md only)
	if name == "" {
		return runInitRefresh(cmd)
	}

	// Full onboard mode
	return runInitOnboard(cmd)
}

// runInitRefresh rewrites the devstack section of AGENTS.md in the current directory.
func runInitRefresh(cmd *cobra.Command) error {
	defaultService := viper.GetString("default_service")
	workspacePath := viper.GetString("workspace")

	if workspacePath == "" {
		if ws, err := workspace.DetectFromCwd(); err == nil {
			workspacePath = ws.Path
		}
	}

	if defaultService == "" {
		if cwd, err := os.Getwd(); err == nil {
			defaultService = filepath.Base(cwd)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	if err := writeAgentsMD(defaultService, cwd, workspacePath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ AGENTS.md updated for service %q\n", defaultService)
	return nil
}

// runInitAll refreshes AGENTS.md for every service registered in the workspace.
func runInitAll() error {
	ws, err := workspace.DetectFromCwd()
	if err != nil {
		return fmt.Errorf("could not detect workspace from current directory: %w\nRun from within a registered workspace.", err)
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return fmt.Errorf("failed to load workspace config: %w", err)
	}

	if len(cfg.ServicePaths) == 0 {
		fmt.Fprintln(os.Stderr, "No services registered in this workspace. Run 'devstack init --name=...' to add one.")
		return nil
	}

	services := make([]string, 0, len(cfg.ServicePaths))
	for name := range cfg.ServicePaths {
		services = append(services, name)
	}
	sort.Strings(services)

	fmt.Fprintf(os.Stderr, "Refreshing AGENTS.md for %d services in workspace '%s'\n", len(services), ws.Name)

	var errs []string
	for _, svcName := range services {
		svcPath := cfg.ServicePaths[svcName]
		if err := writeAgentsMD(svcName, svcPath, ws.Path); err != nil {
			errs = append(errs, fmt.Sprintf("  %s: %v", svcName, err))
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", svcName, err)
		} else {
			fmt.Fprintf(os.Stderr, "✓ %s\n", svcName)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d service(s) failed:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return nil
}

// runInitOnboard registers a new service and wires it up (full onboard).
func runInitOnboard(cmd *cobra.Command) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	name, _ := cmd.Flags().GetString("name")
	path, _ := cmd.Flags().GetString("path")
	port, _ := cmd.Flags().GetInt("port")
	serveCmd, _ := cmd.Flags().GetString("cmd")
	langFlag, _ := cmd.Flags().GetString("language")
	group, _ := cmd.Flags().GetString("group")
	force, _ := cmd.Flags().GetBool("force")

	if path == "" {
		return fmt.Errorf("--path is required when --name is provided")
	}
	if serveCmd == "" {
		return fmt.Errorf("--cmd is required when --name is provided")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("--path %q does not exist: %w", path, err)
	}

	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	// Language detection
	lang := langFlag
	if lang == "" {
		lang = detectLanguage(path)
		fmt.Fprintf(os.Stderr, "Auto-detected language: %s\n", lang)
	}

	// This workspace must be manifest-based — init writes manifests, and the
	// Tiltfile is generated from them.
	if !config.HasWorkspaceManifest(ws.Path) {
		return fmt.Errorf("workspace %q has no %s — run 'devstack workspace add' first", ws.Name, config.WorkspaceManifestFileName)
	}

	// Language-default runtime env for the service manifest.
	langEnv := map[string]string{}
	switch lang {
	case "dotnet":
		langEnv["ASPNETCORE_ENVIRONMENT"] = "Development"
	case "python":
		langEnv["APP_ENV"] = "Development"
	case "node":
		langEnv["NODE_ENV"] = "development"
	}

	// 1. Write the service manifest — the source of truth for how it runs.
	manifestPath := config.ServiceManifestPath(path)
	if _, statErr := os.Stat(manifestPath); statErr == nil && !force {
		return fmt.Errorf("service %q already has %s\nUse --force to overwrite", name, config.ServiceManifestFileName)
	}
	if err := writeServiceManifest(path, name, serveCmd, port, langEnv); err != nil {
		return fmt.Errorf("failed to write service manifest: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Wrote %s\n", manifestPath)

	// 2. Register the repo in the workspace manifest's repos list.
	rel := path
	if r, relErr := filepath.Rel(ws.Path, path); relErr == nil {
		rel = "./" + filepath.ToSlash(r)
	}
	if err := config.AddServiceRepo(ws.Path, rel); err != nil {
		return fmt.Errorf("failed to register service in workspace manifest: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Registered %s in %s\n", rel, config.WorkspaceManifestFileName)

	// 3. Write .mcp.json for AI agent access.
	mcpFile := filepath.Join(path, ".mcp.json")
	if _, err := os.Stat(mcpFile); os.IsNotExist(err) || force {
		if err := writeMCPJson(mcpFile, name); err != nil {
			return fmt.Errorf("failed to write .mcp.json: %w", err)
		}
		fmt.Fprintf(os.Stderr, "✓ Wrote .mcp.json\n")
	} else {
		fmt.Fprintf(os.Stderr, ".mcp.json already exists — skipping (use --force to overwrite)\n")
	}

	// 4. Write AGENTS.md instructions.
	if err := writeAgentsMD(name, path, ws.Path); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write AGENTS.md: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "✓ Wrote AGENTS.md\n")
	}

	// 5. Regenerate the Tiltfile so the daemon picks up the new service.
	if _, err := regenerateTiltfile(ws); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to regenerate Tiltfile: %v\n", err)
	}

	// Summary
	fmt.Printf("\n✓ %q registered in workspace %q\n\n", name, ws.Name)
	fmt.Printf("  manifest:   %s\n", manifestPath)
	fmt.Printf("  .mcp.json:  %s\n", mcpFile)
	fmt.Printf("  AGENTS.md:  %s\n", filepath.Join(path, "AGENTS.md"))
	fmt.Printf("\nNext:\n")
	if group != "" {
		fmt.Printf("  devstack groups add %s %s\n", group, name)
	}
	fmt.Printf("  devstack deps add %s <dep>   # declare dependencies\n", name)
	fmt.Printf("  devstack start %s            # start it\n", name)

	return nil
}

// writeServiceManifest writes a filled devstack.service.yaml for a service with
// a known run command (and optional port/env), unlike the placeholder scaffold.
func writeServiceManifest(dir, name, command string, port int, env map[string]string) error {
	target := config.ServiceManifestPath(dir)
	var sb strings.Builder
	sb.WriteString("version: 1\n\n")
	sb.WriteString("service:\n")
	fmt.Fprintf(&sb, "  name: %s\n\n", name)
	sb.WriteString("runtime:\n")
	sb.WriteString("  run:\n")
	fmt.Fprintf(&sb, "    command: %q\n", command)
	if port > 0 {
		sb.WriteString("  healthcheck:\n")
		sb.WriteString("    type: http\n")
		fmt.Fprintf(&sb, "    port: %d\n", port)
		sb.WriteString("    path: /\n")
		sb.WriteString("    periodSecs: 5\n")
		sb.WriteString("    failureThreshold: 10\n")
	}
	if port > 0 {
		sb.WriteString("\nports:\n")
		fmt.Fprintf(&sb, "  http: %d\n", port)
	}
	if len(env) > 0 {
		sb.WriteString("\nenv:\n  values:\n")
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&sb, "    %s: %q\n", k, env[k])
		}
	}
	if port > 0 {
		sb.WriteString("\nlinks:\n")
		fmt.Fprintf(&sb, "  - url: http://localhost:%d\n", port)
		fmt.Fprintf(&sb, "    label: %s\n", name)
	}
	return os.WriteFile(target, []byte(sb.String()), 0644)
}

// detectLanguage inspects a directory and returns a language string.
func detectLanguage(path string) string {
	checks := []struct {
		glob string
		lang string
	}{
		{"*.csproj", "dotnet"},
		{"requirements.txt", "python"},
		{"*.py", "python"},
		{"package.json", "node"},
		{"go.mod", "go"},
	}
	for _, c := range checks {
		matches, err := filepath.Glob(filepath.Join(path, c.glob))
		if err == nil && len(matches) > 0 {
			return c.lang
		}
	}
	return "unknown"
}

// writeMCPJson creates a .mcp.json file in the service directory.
func writeMCPJson(mcpFile, serviceName string) error {
	serviceDir := filepath.Dir(mcpFile)
	if identity, err := config.ResolveIdentity(serviceDir); err == nil && identity.ServiceName != "" {
		serviceName = identity.ServiceName
	}
	type mcpEntry struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	type mcpConfig struct {
		McpServers map[string]mcpEntry `json:"mcpServers"`
	}
	cfg := mcpConfig{
		McpServers: map[string]mcpEntry{
			"devstack": {
				Type:    "stdio",
				Command: "devstack",
				Args:    []string{"serve", "--transport=stdio"},
				Env: map[string]string{
					"DEVSTACK_DEFAULT_SERVICE": serviceName,
				},
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(mcpFile, append(data, '\n'), 0644)
}

// writeAgentsMD strips and rewrites the devstack section of AGENTS.md.
func writeAgentsMD(serviceName, servicePath, workspacePath string) error {
	agentsFile := filepath.Join(servicePath, "AGENTS.md")
	existing, err := os.ReadFile(agentsFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	const sectionHeader = "## Dev Stack (devstack MCP)"
	stripped := string(existing)
	if idx := strings.Index(stripped, "\n"+sectionHeader); idx != -1 {
		stripped = stripped[:idx]
	} else if strings.HasPrefix(stripped, sectionHeader) {
		stripped = ""
	}
	instructions := buildAgentInstructions(serviceName, workspacePath)
	return os.WriteFile(agentsFile, []byte(stripped+instructions), 0644)
}

func buildAgentInstructions(defaultService string, workspacePath string) string {
	contextLine := ""
	if workspacePath != "" {
		contextLine += fmt.Sprintf("Workspace: `%s`", workspacePath)
	}
	if defaultService != "" {
		if contextLine != "" {
			contextLine += " · "
		}
		contextLine += fmt.Sprintf("Default service: `%s`", defaultService)
	}
	if contextLine != "" {
		contextLine += "\n\n"
	}

	startCmd := "devstack start <service>    # start a service + its dependencies"
	if defaultService != "" {
		startCmd = fmt.Sprintf("devstack start %-19s # start this service + its dependencies", defaultService)
	}

	observabilityBlock := "**Observability:** Not enabled for this workspace — services are not assumed to be OTEL-instrumented and no collector runs. " +
		"To turn it on, set `observability.enabled: true` in the workspace manifest, then `devstack otel start`.\n\n"
	if config.ObservabilityEnabled(workspacePath) {
		observabilityBlock = "**Observability:** Services send traces/logs to the local collector (gRPC `localhost:4317`). " +
			"The collector routes telemetry upstream — configure with `devstack otel configure`. " +
			"When no upstream is set, the collector runs in debug mode and telemetry is visible in collector logs. " +
			"Per-developer endpoint override: set `OTEL_EXPORTER_OTLP_ENDPOINT` in `.envrc`.\n\n"
	}

	return "\n## Dev Stack (devstack MCP)\n\n" +
		"devstack is a CLI and MCP server that gives agents programmatic control over a local development stack. " +
		"It sits on top of [Tilt](https://tilt.dev) to manage service lifecycle, dependency ordering, and observability.\n\n" +
		"**Concepts:**\n\n" +
		"| Term | Meaning |\n" +
		"|------|---------|\n" +
		"| **Workspace** | A directory of interlinked services sharing a dev daemon and OTEL stack |\n" +
		"| **Service** | A single runnable process — API, worker, importer, etc. — managed by the dev daemon |\n" +
		"| **Group** | A named set of services you can start/stop together |\n" +
		"| **Dependency** | An ordering constraint: service A won't start until service B is running |\n\n" +
		"Local dev only. devstack controls local services and local observability only.\n\n" +
		contextLine +
		"```bash\n" +
		"devstack status                     # live service state\n" +
		"devstack topology                   # services, groups, deps, dependents\n" +
		"devstack otel status                # collector state + telemetry evidence\n" +
		"devstack workspace doctor           # config and topology checks\n" +
		"devstack workspace up               # start the local daemon\n" +
		"devstack workspace down             # stop the local daemon\n" +
		startCmd + "\n" +
		"```\n\n" +
		"Use the MCP tools from `.mcp.json` as discovery helpers, not as hidden sources of truth.\n\n" +
		"Rules:\n" +
		"1. Check topology before making dependency claims.\n" +
		"2. Check telemetry status before inferring from missing traces or logs.\n" +
		"3. Use process logs or runtime state when telemetry is partial or inconclusive.\n" +
		"4. Do not use devstack against staging or production.\n\n" +
		observabilityBlock +
		"### First-time setup on a new machine\n\n" +
		"If devstack MCP tools aren't responding or the workspace isn't registered yet, run:\n\n" +
		"```bash\n" +
		"devstack workspace add <path>        # register a workspace — a group of interlinked services under a directory\n" +
		"devstack workspace up                # start Tilt + OTEL collector\n" +
		"devstack init --all                  # regenerate .mcp.json and AGENTS.md for all services\n" +
		"devstack status                      # verify\n" +
		"```\n\n" +
		"If services aren't registered yet (no `.devstack.json` in the workspace):\n\n" +
		"```bash\n" +
		"devstack init --name=<service> --path=<path> --cmd=\"<start command>\" --port=<port>\n" +
		"```\n"
}
