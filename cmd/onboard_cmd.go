package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"devstack/internal/config"
	"devstack/internal/workspace"
)

var onboardCmd = &cobra.Command{
	Use:   "onboard <service-name> <service-path>",
	Short: "Onboard a new service into the workspace Tiltfile and devstack config",
	Long: `Onboard a new service into the workspace Tiltfile (.mcp.json and AGENTS.md in the service directory).

Detects the service language automatically from files in <service-path>:
  - *.csproj  → dotnet
  - go.mod    → go
  - requirements.txt → python
  - package.json     → node

Use --lang to override auto-detection.
Use --port to specify the HTTP port the service listens on (used for readiness probe).
Use --label to set the Tiltfile label (default: auto-detected language).
Use --workspace to specify the workspace (default: auto-detect from cwd).`,
	Args: cobra.ExactArgs(2),
	RunE: runOnboard,
}

func init() {
	rootCmd.AddCommand(onboardCmd)
	onboardCmd.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
	onboardCmd.Flags().String("lang", "", "Language override: dotnet, go, python, node")
	onboardCmd.Flags().Int("port", 0, "HTTP port the service listens on (used for readiness probe; 0 = omit probe)")
	onboardCmd.Flags().String("label", "", "Tiltfile label (default: auto-detected language)")
	onboardCmd.Flags().String("serve-cmd", "", "Override the serve_cmd in the Tiltfile block")
}

func runOnboard(cmd *cobra.Command, args []string) error {
	serviceName := args[0]
	servicePath := args[1]

	// Resolve service path to absolute
	absServicePath, err := filepath.Abs(servicePath)
	if err != nil {
		return fmt.Errorf("failed to resolve service path: %w", err)
	}

	// Verify service path exists
	if _, err := os.Stat(absServicePath); err != nil {
		return fmt.Errorf("service path %q does not exist: %w", absServicePath, err)
	}

	wsFlag, _ := cmd.Flags().GetString("workspace")
	langFlag, _ := cmd.Flags().GetString("lang")
	port, _ := cmd.Flags().GetInt("port")
	labelFlag, _ := cmd.Flags().GetString("label")
	serveCmdFlag, _ := cmd.Flags().GetString("serve-cmd")

	// Resolve workspace
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	// Auto-detect language if not provided
	lang := langFlag
	if lang == "" {
		lang, err = detectLanguage(absServicePath)
		if err != nil {
			return fmt.Errorf("language auto-detection failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Auto-detected language: %s\n", lang)
	}

	// Determine label
	label := labelFlag
	if label == "" {
		label = lang
	}

	// Build Tiltfile block
	tiltBlock := buildTiltBlock(serviceName, absServicePath, lang, label, port, serveCmdFlag)

	// Append to Tiltfile
	tiltfilePath := filepath.Join(ws.Path, "Tiltfile")
	f, err := os.OpenFile(tiltfilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open Tiltfile at %s: %w", tiltfilePath, err)
	}
	_, writeErr := f.WriteString(tiltBlock)
	f.Close()
	if writeErr != nil {
		return fmt.Errorf("failed to append to Tiltfile: %w", writeErr)
	}
	fmt.Fprintf(os.Stderr, "✓ Appended local_resource(%q) to %s\n", serviceName, tiltfilePath)

	// Register service path in .devstack.json
	cfg, err := config.Load(ws.Path)
	if err != nil {
		return fmt.Errorf("failed to load devstack config: %w", err)
	}
	cfg.ServicePaths[serviceName] = absServicePath
	if err := config.Save(ws.Path, cfg); err != nil {
		return fmt.Errorf("failed to save devstack config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Registered service path in .devstack.json\n")

	// Write .mcp.json in the service directory
	if err := writeMCPJson(absServicePath, ws); err != nil {
		return fmt.Errorf("failed to write .mcp.json: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Wrote .mcp.json to %s\n", absServicePath)

	// Append devstack instructions to AGENTS.md in service directory
	if err := writeAgentsMd(absServicePath, serviceName, ws); err != nil {
		return fmt.Errorf("failed to write AGENTS.md: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Appended devstack instructions to AGENTS.md in %s\n", absServicePath)

	fmt.Printf("✓ Onboarded service %q (lang: %s, workspace: %s)\n", serviceName, lang, ws.Name)
	return nil
}

// detectLanguage returns the language identifier for the service at the given path.
func detectLanguage(path string) (string, error) {
	checks := []struct {
		glob string
		lang string
	}{
		{"*.csproj", "dotnet"},
		{"go.mod", "go"},
		{"requirements.txt", "python"},
		{"package.json", "node"},
	}

	for _, c := range checks {
		matches, err := filepath.Glob(filepath.Join(path, c.glob))
		if err != nil {
			return "", fmt.Errorf("glob %q failed: %w", c.glob, err)
		}
		if len(matches) > 0 {
			return c.lang, nil
		}
	}

	return "", fmt.Errorf("could not detect language in %q — use --lang to specify (dotnet, go, python, node)", path)
}

// buildTiltBlock builds the Tiltfile local_resource block for the service.
func buildTiltBlock(name, path, lang, label string, port int, serveCmdOverride string) string {
	var sb strings.Builder

	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("local_resource(\n"))
	sb.WriteString(fmt.Sprintf("    %q,\n", name))

	// Build serve_cmd
	serveCmd := serveCmdOverride
	if serveCmd == "" {
		switch lang {
		case "dotnet":
			serveCmd = "DOTNET + \" run\""
		case "go":
			serveCmd = "\"go run ./...\""
		case "python":
			serveCmd = "\"bash -c \\\"source .envrc && python main.py\\\"\""
		case "node":
			serveCmd = "\"npm start\""
		default:
			serveCmd = fmt.Sprintf("%q", "bash -c \"./run.sh\"")
		}
	} else {
		serveCmd = fmt.Sprintf("%q", serveCmd)
	}

	// For dotnet we use the DOTNET variable — don't quote it as a plain string
	if lang == "dotnet" && serveCmdOverride == "" {
		sb.WriteString(fmt.Sprintf("    serve_cmd=DOTNET + \" run\",\n"))
	} else {
		sb.WriteString(fmt.Sprintf("    serve_cmd=%s,\n", serveCmd))
	}

	sb.WriteString(fmt.Sprintf("    serve_dir=%q,\n", path))

	// serve_env
	sb.WriteString("    serve_env={\n")
	if lang == "dotnet" {
		sb.WriteString("        \"ASPNETCORE_ENVIRONMENT\": ASPNET_ENV,\n")
	}
	sb.WriteString("        \"OTEL_EXPORTER_OTLP_ENDPOINT\": OTEL_ENDPOINT,\n")
	sb.WriteString("    },\n")

	// readiness probe (only if port > 0)
	if port > 0 {
		sb.WriteString(fmt.Sprintf("    readiness_probe=probe(\n"))
		sb.WriteString(fmt.Sprintf("        http_get=http_get_action(port=%d),\n", port))
		sb.WriteString("        period_secs=5,\n")
		sb.WriteString("        failure_threshold=12,\n")
		sb.WriteString("    ),\n")
	}

	sb.WriteString("    trigger_mode=TRIGGER_MODE_MANUAL,\n")
	sb.WriteString("    auto_init=False,\n")
	sb.WriteString(fmt.Sprintf("    labels=[%q],\n", label))
	sb.WriteString(")\n")

	return sb.String()
}

// mcpServerEntry is the structure of a single entry in .mcp.json servers map.
type mcpServerEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// mcpConfig is the top-level .mcp.json structure.
type mcpConfig struct {
	McpServers map[string]mcpServerEntry `json:"mcpServers"`
}

// writeMCPJson writes (or merges) a .mcp.json file in the service directory
// that wires up the devstack MCP server for this workspace.
func writeMCPJson(servicePath string, ws *workspace.Workspace) error {
	mcpFile := filepath.Join(servicePath, ".mcp.json")

	var cfg mcpConfig

	// Load existing file if present
	if data, err := os.ReadFile(mcpFile); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("failed to parse existing .mcp.json: %w", err)
		}
	}

	if cfg.McpServers == nil {
		cfg.McpServers = map[string]mcpServerEntry{}
	}

	// Add/overwrite the devstack entry
	cfg.McpServers["devstack"] = mcpServerEntry{
		Type:    "stdio",
		Command: "devstack",
		Args:    []string{"serve", "--tilt-port", strconv.Itoa(ws.TiltPort), "--workspace", ws.Path},
		Env:     map[string]string{"DEVSTACK_WORKSPACE": ws.Path},
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

// writeAgentsMd appends devstack instructions to AGENTS.md in the service directory.
func writeAgentsMd(servicePath, serviceName string, ws *workspace.Workspace) error {
	agentsFile := filepath.Join(servicePath, "AGENTS.md")

	// Check if already contains our section
	existing, err := os.ReadFile(agentsFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	if strings.Contains(string(existing), "## Dev Stack (devstack MCP)") {
		fmt.Fprintf(os.Stderr, "AGENTS.md already contains devstack MCP instructions — skipping.\n")
		return nil
	}

	f, err := os.OpenFile(agentsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open AGENTS.md: %w", err)
	}
	defer f.Close()

	instructions := buildInstructions(serviceName, ws.Path)
	if _, err := f.WriteString(instructions); err != nil {
		return fmt.Errorf("failed to write AGENTS.md: %w", err)
	}

	return nil
}
