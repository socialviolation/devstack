package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"devstack/internal/config"
	"devstack/internal/workspace"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Register a service and write AI agent instructions",
	Long: `Register a service into the workspace and write AI agent instructions to AGENTS.md.

FIRST-TIME SERVICE SETUP (provide --name, --path, --cmd)
  Registers a new service so devstack can run and observe it:
    1. Adds the service to the workspace run configuration
    2. Creates .mcp.json to wire up the devstack MCP server for AI agents
    3. Writes AGENTS.md with instructions for AI agents on how to run and observe it
    4. Registers the service path so 'devstack start' auto-detects it from the directory
    5. Sets OTEL_EXPORTER_OTLP_ENDPOINT so traces ship to the workspace SigNoz stack

  Use --force to overwrite an existing service entry (e.g. to update the run command).

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
	initCmd.Flags().String("group", "", "Group to add the service to in .devstack.json")
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

	// Build env map
	serveEnv := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": workspace.OtelOTLPEndpoint(ws),
	}
	switch lang {
	case "dotnet":
		serveEnv["ASPNETCORE_ENVIRONMENT"] = "Development"
	case "python":
		serveEnv["APP_ENV"] = "Development"
	case "node":
		serveEnv["NODE_ENV"] = "development"
	}

	// Write to Tiltfile
	tiltfilePath := filepath.Join(ws.Path, "Tiltfile")
	tiltBlock := buildTiltBlock(name, serveCmd, path, lang, port, serveEnv)

	existing := tiltfileHasService(tiltfilePath, name)
	if existing && !force {
		return fmt.Errorf("service %q already exists in the workspace configuration\nUse --force to overwrite", name)
	}
	if existing && force {
		if err := replaceTiltfileService(tiltfilePath, name, tiltBlock); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "✓ Updated service %q in workspace configuration\n", name)
	} else {
		if err := appendToTiltfile(tiltfilePath, tiltBlock); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "✓ Added service %q to workspace configuration\n", name)
	}

	// Write .mcp.json
	mcpFile := filepath.Join(path, ".mcp.json")
	if _, err := os.Stat(mcpFile); os.IsNotExist(err) || force {
		if err := writeMCPJson(mcpFile, name, ws); err != nil {
			return fmt.Errorf("failed to write .mcp.json: %w", err)
		}
		fmt.Fprintf(os.Stderr, "✓ Wrote .mcp.json\n")
	} else {
		fmt.Fprintf(os.Stderr, ".mcp.json already exists — skipping (use --force to overwrite)\n")
	}

	// Write AGENTS.md
	if err := writeAgentsMD(name, path, ws.Path); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write AGENTS.md: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "✓ Wrote AGENTS.md\n")
	}

	// Update .devstack.json
	cfg, err := config.Load(ws.Path)
	if err != nil {
		return fmt.Errorf("failed to load workspace config: %w", err)
	}
	cfg.ServicePaths[name] = path
	if group != "" {
		// Avoid duplicates
		found := false
		for _, s := range cfg.Groups[group] {
			if s == name {
				found = true
				break
			}
		}
		if !found {
			cfg.Groups[group] = append(cfg.Groups[group], name)
		}
	}
	if err := config.Save(ws.Path, cfg); err != nil {
		return fmt.Errorf("failed to save workspace config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Registered in workspace config\n")

	// Summary
	fmt.Printf("\n✓ %q registered in workspace %q\n\n", name, ws.Name)
	fmt.Printf("  .mcp.json:  %s\n", mcpFile)
	fmt.Printf("  AGENTS.md:  %s\n", filepath.Join(path, "AGENTS.md"))
	if group != "" {
		fmt.Printf("  Group:      %s\n", group)
	}
	fmt.Printf("\nNext:\n")
	fmt.Printf("  devstack deps add %s <dep>   # declare dependencies\n", name)
	fmt.Printf("  devstack start %s            # start it\n", name)

	return nil
}

// tiltfileHasService checks if a service block already exists in the Tiltfile.
func tiltfileHasService(tiltfilePath, name string) bool {
	data, err := os.ReadFile(tiltfilePath)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), fmt.Sprintf("# %s\n", name)) ||
		strings.Contains(string(data), fmt.Sprintf("%q,", name))
}

// replaceTiltfileService removes the old block for a service and appends the new one.
func replaceTiltfileService(tiltfilePath, name, newBlock string) error {
	data, err := os.ReadFile(tiltfilePath)
	if err != nil {
		return fmt.Errorf("failed to read Tiltfile: %w", err)
	}

	content := string(data)
	marker := fmt.Sprintf("\n# %s\n", name)
	start := strings.Index(content, marker)
	if start == -1 {
		// Block not found via comment marker — just append
		return appendToTiltfile(tiltfilePath, newBlock)
	}

	// Find the closing ')' of the local_resource block
	end := start + len(marker)
	depth := 0
	found := false
	for i := end; i < len(content); i++ {
		switch content[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				end = i + 1
				// consume trailing newline
				if end < len(content) && content[end] == '\n' {
					end++
				}
				found = true
			}
			depth--
		}
		if found {
			break
		}
	}

	if !found {
		return appendToTiltfile(tiltfilePath, newBlock)
	}

	updated := content[:start] + newBlock + content[end:]
	return os.WriteFile(tiltfilePath, []byte(updated), 0644)
}

// appendToTiltfile appends a block to the Tiltfile.
func appendToTiltfile(tiltfilePath, block string) error {
	f, err := os.OpenFile(tiltfilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open workspace configuration at %s: %w", tiltfilePath, err)
	}
	_, writeErr := f.WriteString(block)
	f.Close()
	return writeErr
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

// buildTiltBlock builds the local_resource block for a service.
func buildTiltBlock(name, serveCmd, path, lang string, port int, serveEnv map[string]string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n# %s\n", name))
	sb.WriteString("local_resource(\n")
	sb.WriteString(fmt.Sprintf("    %q,\n", name))
	sb.WriteString(fmt.Sprintf("    serve_cmd=%q,\n", serveCmd))
	sb.WriteString(fmt.Sprintf("    serve_dir=%q,\n", path))
	sb.WriteString("    serve_env={\n")
	sb.WriteString(fmt.Sprintf("        %q: %q,\n", "OTEL_EXPORTER_OTLP_ENDPOINT", serveEnv["OTEL_EXPORTER_OTLP_ENDPOINT"]))
	for k, v := range serveEnv {
		if k == "OTEL_EXPORTER_OTLP_ENDPOINT" {
			continue
		}
		sb.WriteString(fmt.Sprintf("        %q: %q,\n", k, v))
	}
	sb.WriteString("    },\n")
	sb.WriteString("    trigger_mode=TRIGGER_MODE_MANUAL,\n")
	sb.WriteString("    auto_init=False,\n")
	sb.WriteString(fmt.Sprintf("    labels=[%q],\n", lang))
	if port > 0 {
		sb.WriteString(fmt.Sprintf("    readiness_probe=probe(http_get_action(port=%d), period_secs=5, failure_threshold=10),\n", port))
		sb.WriteString(fmt.Sprintf("    links=[link(%q, %q)],\n", fmt.Sprintf("http://localhost:%d", port), name))
	}
	sb.WriteString(")\n")
	return sb.String()
}

// writeMCPJson creates a .mcp.json file in the service directory.
func writeMCPJson(mcpFile, serviceName string, ws *workspace.Workspace) error {
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
					"TILT_PORT":                strconv.Itoa(ws.TiltPort),
					"DEVSTACK_WORKSPACE":       ws.Path,
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
		contextLine = contextLine + "\n\n"
	}

	startCmd := "devstack start <service>    # start a service + its dependencies"
	if defaultService != "" {
		startCmd = fmt.Sprintf("devstack start %-19s # start this service + its dependencies", defaultService)
	}

	return "\n## Dev Stack (devstack MCP)\n\n" +
		"devstack is a local development service manager. It runs your services,\n" +
		"resolves startup dependencies, and ships OpenTelemetry traces and logs to a\n" +
		"local SigNoz observability stack. AI agents interact with it through MCP tools\n" +
		"that expose live service state and correlated trace/log data.\n\n" +
		"**LOCAL DEV ONLY.** devstack controls local processes only. Never use it to\n" +
		"investigate or modify staging or production environments.\n\n" +
		contextLine +
		"### Starting the daemon and services\n\n" +
		"The MCP tools require the dev daemon to be running. Check status first;\n" +
		"start the daemon if it is stopped, then start the services you need.\n\n" +
		"```bash\n" +
		"devstack status                     # show live state of all services\n" +
		"devstack workspace up               # start the dev daemon (if not running)\n" +
		"devstack workspace down             # stop the dev daemon\n" +
		startCmd + "\n" +
		"devstack start --group=<group>      # start all services in a group (resolves deps)\n" +
		"devstack stop <service>             # stop a service\n" +
		"devstack init --all                 # refresh AGENTS.md in every service repo\n" +
		"devstack otel open                  # open the SigNoz trace UI in the browser\n" +
		"```\n\n" +
		"Auto-detection: `start` and `stop` detect the service from the current directory\n" +
		"when no service name is given.\n\n" +
		"### MCP tools\n\n" +
		"These tools are available to you via the configured MCP server. The `.mcp.json`\n" +
		"in this repo wires them up automatically — no manual configuration needed.\n\n" +
		"| Tool | When to use it |\n" +
		"|------|----------------|\n" +
		"| `investigate` | **Start here when something is broken.** Correlates traces and logs in one call to pinpoint the root cause. |\n" +
		"| `status` | Check live state of all services — running/error/idle, ports, deps. |\n" +
		"| `process_logs` | Fetch raw stdout/stderr from a service. Use `errors_only=true` to filter noise. |\n" +
		"| `traces` | Query recent distributed traces from SigNoz. |\n" +
		"| `errors` | Surface recent error-level spans across all services. |\n" +
		"| `restart` | Trigger a rebuild and restart after a code change. |\n" +
		"| `stop` | Disable a running service. |\n" +
		"| `configure` | Set a runtime config value for a service. |\n\n" +
		"### Rules\n\n" +
		"1. **Call `investigate` first** when something is broken — it correlates traces\n" +
		"   and logs in a single call and is faster than querying each tool separately.\n" +
		"2. **Stop only what you started** — do not tear down the whole stack unless\n" +
		"   the user explicitly asks for it.\n" +
		"3. **Never use devstack for prod or staging** — it only controls local services.\n"
}
