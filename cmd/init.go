package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"devstack/internal/workspace"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Append dev stack MCP instructions to AGENTS.md",
	Long:  `Appends LLM instructions for the navexa_dev MCP tools to AGENTS.md in the current directory.`,
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	defaultService := viper.GetString("default_service")
	workspacePath := viper.GetString("workspace")

	agentsFile := filepath.Join(".", "AGENTS.md")

	// Check if file already contains our section to avoid duplicates
	existing, err := os.ReadFile(agentsFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	if strings.Contains(string(existing), "## Dev Stack (navexa_dev MCP)") {
		fmt.Fprintln(os.Stderr, "AGENTS.md already contains navexa_dev MCP instructions — skipping.")
	} else {
		f, err := os.OpenFile(agentsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("failed to open AGENTS.md: %w", err)
		}
		defer f.Close()

		_, err = f.WriteString(buildInstructions(defaultService, workspacePath))
		if err != nil {
			return fmt.Errorf("failed to write to AGENTS.md: %w", err)
		}

		fmt.Fprintf(os.Stderr, "✓ navexa_dev MCP instructions appended to %s\n", agentsFile)
	}

	// Inject Stop hook into .claude/settings.local.json
	if defaultService == "" {
		fmt.Fprintln(os.Stderr, "No default service configured — skipping Stop hook injection.")
	} else {
		if err := injectStopHook(defaultService, workspacePath); err != nil {
			return fmt.Errorf("failed to inject Stop hook: %w", err)
		}
	}

	// Auto-register workspace if DEVSTACK_WORKSPACE is set
	if workspacePath == "" {
		fmt.Fprintln(os.Stderr, "No workspace configured (DEVSTACK_WORKSPACE not set) — skipping workspace registration.")
		return nil
	}

	absPath, err := filepath.Abs(workspacePath)
	if err != nil {
		return fmt.Errorf("failed to resolve workspace path: %w", err)
	}

	ws := workspace.Workspace{
		Name:     filepath.Base(absPath),
		Path:     absPath,
		TiltPort: 0, // auto-assign
	}
	if err := workspace.Register(ws); err != nil {
		return fmt.Errorf("failed to register workspace: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✓ Workspace '%s' registered at %s\n", ws.Name, absPath)
	return nil
}

// claudeSettings represents the structure of .claude/settings.local.json
type claudeSettings struct {
	Hooks map[string][]hookEntry `json:"hooks,omitempty"`
	// Preserve unknown fields
	Extra map[string]json.RawMessage `json:"-"`
}

type hookEntry struct {
	Matcher string     `json:"matcher"`
	Hooks   []hookItem `json:"hooks"`
}

type hookItem struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func injectStopHook(defaultService string, workspacePath string) error {
	claudeDir := filepath.Join(".", ".claude")
	settingsFile := filepath.Join(claudeDir, "settings.local.json")
	hookCommand := fmt.Sprintf("devstack stop --default-service=%s --if-last-session --workspace=%s", defaultService, workspacePath)

	// Load existing settings (or start fresh)
	var rawData map[string]json.RawMessage
	existingBytes, err := os.ReadFile(settingsFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read settings.local.json: %w", err)
	}

	if len(existingBytes) > 0 {
		if err := json.Unmarshal(existingBytes, &rawData); err != nil {
			return fmt.Errorf("failed to parse settings.local.json: %w", err)
		}
	}
	if rawData == nil {
		rawData = make(map[string]json.RawMessage)
	}

	// Parse existing hooks map
	var hooksMap map[string][]hookEntry
	if hooksRaw, ok := rawData["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &hooksMap); err != nil {
			return fmt.Errorf("failed to parse hooks in settings.local.json: %w", err)
		}
	}
	if hooksMap == nil {
		hooksMap = make(map[string][]hookEntry)
	}

	// Check if our Stop hook already exists (idempotent)
	for _, entry := range hooksMap["Stop"] {
		for _, h := range entry.Hooks {
			if h.Command == hookCommand {
				fmt.Fprintln(os.Stderr, "Stop hook already present in .claude/settings.local.json — skipping.")
				return nil
			}
		}
	}

	// Append our Stop hook entry
	hooksMap["Stop"] = append(hooksMap["Stop"], hookEntry{
		Matcher: "",
		Hooks: []hookItem{
			{Type: "command", Command: hookCommand},
		},
	})

	// Marshal hooks back and merge into rawData
	hooksBytes, err := json.Marshal(hooksMap)
	if err != nil {
		return fmt.Errorf("failed to marshal hooks: %w", err)
	}
	rawData["hooks"] = json.RawMessage(hooksBytes)

	// Write back to file with indentation
	output, err := json.MarshalIndent(rawData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	// Create .claude/ directory if needed
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude/ directory: %w", err)
	}

	if err := os.WriteFile(settingsFile, append(output, '\n'), 0644); err != nil {
		return fmt.Errorf("failed to write settings.local.json: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✓ Stop hook injected into .claude/settings.local.json (service: %s)\n", defaultService)
	return nil
}

func buildInstructions(defaultService string, workspacePath string) string {
	defaultServiceLine := ""
	if defaultService != "" {
		defaultServiceLine = fmt.Sprintf("The default service for this repo is `%s` — tool calls omitting `name` use it automatically.\n\n", defaultService)
	}

	workspaceLine := ""
	if workspacePath != "" {
		workspaceLine = fmt.Sprintf("This repo belongs to the devstack workspace at `%s` (Tilt manages all services within it).\n", workspacePath)
	}

	stopHookLine := ""
	if defaultService != "" {
		stopHookLine = fmt.Sprintf("\nA Stop hook will automatically stop `%s` when your Claude session ends (skipped if other sessions are active).\n", defaultService)
	}

	tools := "" +
		"| Tool | Args | Use when |\n" +
		"|------|------|----------|\n" +
		"| `status` | — | User asks what's running, what's broken, or the state of the stack |\n" +
		"| `start` | `name` (optional) | User wants to start or run a service |\n" +
		"| `restart` | `name` (optional) | User wants to restart or rebuild a service |\n" +
		"| `stop` | `name` (optional) | User wants to stop or kill a service |\n" +
		"| `start_all` | `services` (optional, comma-separated) | User wants to spin up the whole stack or multiple services |\n" +
		"| `stop_all` | — | User wants to tear down the stack |\n" +
		"| `logs` | `name` (optional), `lines` (default 100) | User wants to see output or recent activity from a service |\n" +
		"| `errors` | `name` (optional), `lines` (default 50) | User asks if there are errors, or something seems broken |\n" +
		"| `what_happened` | `name` (optional), `since_minutes` (default 15) | User asks what went wrong, why something failed, or \"what happened to X\" |\n" +
		"| `set_environment` | `env`: Development or Production | User wants to switch the stack between dev and prod config |\n"

	return "\n## Dev Stack (navexa_dev MCP)\n\n" +
		"You have access to the **navexa_dev** MCP server which controls the Navexa dev stack.\n" +
		workspaceLine +
		defaultServiceLine +
		"\nCall `status` to see all running services and their current state. Service names are discovered live from Tilt — do not guess them.\n\n" +
		"Tilt must be running for tools to work. If a tool returns \"Tilt is not running\", ask the user to run `tilt up` in the workspace directory.\n" +
		stopHookLine + "\n" +
		"### Tools\n\n" + tools + "\n" +
		"### Environment switching\n\n" +
		"`set_environment` switches .NET services and the Python importer between Development and Production.\n" +
		"The frontend (navexa-frontend) is NOT affected — it requires a manual rebuild to change environments.\n"
}
