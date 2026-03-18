package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/workspace"
)

var onboardCmd = &cobra.Command{
	Use:   "onboard",
	Short: "Onboard a new service into the workspace Tiltfile and devstack config",
	Long: `Onboard a new service into the workspace Tiltfile and devstack config.

Creates or updates:
  - local_resource block in <workspace>/Tiltfile
  - <service-path>/.mcp.json  (wires up the devstack MCP server)
  - <service-path>/AGENTS.md  (via devstack init)
  - <workspace>/.devstack.json ServicePaths entry

Auto-detects language from files in --path:
  - *.csproj            → dotnet
  - go.mod              → go
  - requirements.txt    → python
  - package.json        → node

Use --language to override auto-detection.
Use --port to specify the HTTP port (adds readiness probe + link).`,
	RunE: runOnboard,
}

func init() {
	rootCmd.AddCommand(onboardCmd)
	onboardCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	onboardCmd.Flags().String("name", "", "Service name as it appears in Tilt (required)")
	onboardCmd.Flags().String("path", "", "Absolute path to service repo (required)")
	onboardCmd.Flags().Int("port", 0, "HTTP port the service listens on (0 = no readiness probe)")
	onboardCmd.Flags().String("cmd", "", "Serve command e.g. \"dotnet run\" (required)")
	onboardCmd.Flags().String("language", "", "One of: dotnet, python, node, go (default: auto-detect)")
	onboardCmd.Flags().String("group", "", ".devstack.json group to add service to (optional)")

	_ = onboardCmd.MarkFlagRequired("name")
	_ = onboardCmd.MarkFlagRequired("path")
	_ = onboardCmd.MarkFlagRequired("cmd")
}

func runOnboard(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")
	name, _ := cmd.Flags().GetString("name")
	path, _ := cmd.Flags().GetString("path")
	port, _ := cmd.Flags().GetInt("port")
	serveCmd, _ := cmd.Flags().GetString("cmd")
	langFlag, _ := cmd.Flags().GetString("language")
	group, _ := cmd.Flags().GetString("group")

	// Step 1: Resolve workspace
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	// Step 2: Validate required flags and path existence
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	if path == "" {
		return fmt.Errorf("--path is required")
	}
	if serveCmd == "" {
		return fmt.Errorf("--cmd is required")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("--path %q does not exist on disk: %w", path, err)
	}

	// Step 3: Auto-detect language if not provided
	lang := langFlag
	if lang == "" {
		lang = onboardDetectLanguage(path)
		fmt.Fprintf(os.Stderr, "Auto-detected language: %s\n", lang)
	}

	// Step 4: Build serve_env map
	serveEnv := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:18889",
	}
	switch lang {
	case "dotnet":
		serveEnv["ASPNETCORE_ENVIRONMENT"] = "Development"
	case "python":
		serveEnv["APP_ENV"] = "Development"
	case "node":
		serveEnv["NODE_ENV"] = "development"
	}

	// Step 5: Append local_resource block to Tiltfile
	tiltfilePath := filepath.Join(ws.Path, "Tiltfile")
	tiltBlock := buildOnboardTiltBlock(name, serveCmd, path, lang, port, serveEnv)
	if err := appendToTiltfile(tiltfilePath, tiltBlock); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ Appended local_resource(%q) to %s\n", name, tiltfilePath)

	// Step 6: Create .mcp.json if it doesn't exist
	mcpFile := filepath.Join(path, ".mcp.json")
	if _, err := os.Stat(mcpFile); os.IsNotExist(err) {
		if err := writeOnboardMCPJson(mcpFile, name, ws); err != nil {
			return fmt.Errorf("failed to write .mcp.json: %w", err)
		}
		fmt.Fprintf(os.Stderr, "✓ Created .mcp.json in %s\n", path)
	} else {
		fmt.Fprintf(os.Stderr, ".mcp.json already exists in %s — skipping\n", path)
	}

	// Step 7: Run devstack init in the service directory (non-fatal)
	initArgs := []string{"init", fmt.Sprintf("--workspace=%s", ws.Path), fmt.Sprintf("--default-service=%s", name)}
	initCmd := exec.Command("devstack", initArgs...)
	initCmd.Dir = path
	out, initErr := initCmd.CombinedOutput()
	if len(out) > 0 {
		fmt.Print(string(out))
	}
	if initErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: devstack init failed: %v\n", initErr)
	}

	// Step 8: Update .devstack.json
	cfg, err := config.Load(ws.Path)
	if err != nil {
		return fmt.Errorf("failed to load devstack config: %w", err)
	}
	cfg.ServicePaths[name] = path
	if group != "" {
		cfg.Groups[group] = append(cfg.Groups[group], name)
	}
	if err := config.Save(ws.Path, cfg); err != nil {
		return fmt.Errorf("failed to save devstack config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Registered service path in .devstack.json\n")

	// Step 9: Print summary
	agentsMd := filepath.Join(path, "AGENTS.md")
	fmt.Printf("\n✓ Service %q onboarded to workspace %q\n\n", name, ws.Name)
	fmt.Printf("  Tiltfile:    %s\n", tiltfilePath)
	fmt.Printf("  .mcp.json:   %s\n", mcpFile)
	fmt.Printf("  AGENTS.md:   %s\n", agentsMd)
	if group != "" {
		fmt.Printf("  Group:       %s\n", group)
	}
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  devstack deps add %s <dependency>   # declare dependencies if needed\n", name)
	fmt.Printf("  devstack start %s                   # start it\n", name)

	return nil
}

// onboardDetectLanguage detects the language of the service at the given path.
// Falls back to "unknown" instead of returning an error.
func onboardDetectLanguage(path string) string {
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

// buildOnboardTiltBlock builds the Tiltfile local_resource block.
func buildOnboardTiltBlock(name, serveCmd, path, lang string, port int, serveEnv map[string]string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\n# %s\n", name))
	sb.WriteString("local_resource(\n")
	sb.WriteString(fmt.Sprintf("    %q,\n", name))
	sb.WriteString(fmt.Sprintf("    serve_cmd=%q,\n", serveCmd))
	sb.WriteString(fmt.Sprintf("    serve_dir=%q,\n", path))

	// serve_env — OTEL first, then language-specific
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

// appendToTiltfile appends a block to the Tiltfile.
func appendToTiltfile(tiltfilePath, block string) error {
	f, err := os.OpenFile(tiltfilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open Tiltfile at %s: %w", tiltfilePath, err)
	}
	_, writeErr := f.WriteString(block)
	f.Close()
	if writeErr != nil {
		return fmt.Errorf("failed to append to Tiltfile: %w", writeErr)
	}
	return nil
}

// onboardMCPEntry is the structure of a single entry in .mcp.json servers map.
type onboardMCPEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// onboardMCPConfig is the top-level .mcp.json structure.
type onboardMCPConfig struct {
	McpServers map[string]onboardMCPEntry `json:"mcpServers"`
}

// writeOnboardMCPJson creates a .mcp.json file in the service directory.
func writeOnboardMCPJson(mcpFile, serviceName string, ws *workspace.Workspace) error {
	cfg := onboardMCPConfig{
		McpServers: map[string]onboardMCPEntry{
			"navexa_dev": {
				Type:    "stdio",
				Command: "devstack",
				Args:    []string{"serve", "--transport=stdio"},
				Env: map[string]string{
					"TILT_PORT":               strconv.Itoa(ws.TiltPort),
					"DEVSTACK_WORKSPACE":      ws.Path,
					"DEVSTACK_DEFAULT_SERVICE": serviceName,
				},
			},
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal .mcp.json: %w", err)
	}

	if err := os.WriteFile(mcpFile, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("failed to write .mcp.json: %w", err)
	}

	return nil
}
