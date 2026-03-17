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
	Long:  `Appends LLM instructions for the devstack MCP tools to AGENTS.md in the current directory.`,
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	defaultService := viper.GetString("default_service")
	workspacePath := viper.GetString("workspace")

	// Auto-detect workspace from cwd if not explicitly set
	if workspacePath == "" {
		if ws, err := workspace.DetectFromCwd(); err == nil {
			workspacePath = ws.Path
			fmt.Fprintf(os.Stderr, "Auto-detected workspace: %s\n", ws.Name)
		}
	}

	// Auto-detect default service from cwd basename if not explicitly set
	if defaultService == "" {
		if cwd, err := os.Getwd(); err == nil {
			defaultService = filepath.Base(cwd)
			fmt.Fprintf(os.Stderr, "Auto-detected default service: %s\n", defaultService)
		}
	}

	agentsFile := filepath.Join(".", "AGENTS.md")

	// Check if file already contains our section to avoid duplicates
	existing, err := os.ReadFile(agentsFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	if strings.Contains(string(existing), "## Dev Stack (devstack MCP)") {
		fmt.Fprintln(os.Stderr, "AGENTS.md already contains devstack MCP instructions — skipping.")
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

		fmt.Fprintf(os.Stderr, "✓ devstack MCP instructions appended to %s\n", agentsFile)
	}

	// Inject Stop hook into .claude/settings.local.json
	if defaultService == "" {
		fmt.Fprintln(os.Stderr, "Could not determine default service — skipping Stop hook injection.")
	} else {
		if err := injectStopHook(defaultService, workspacePath); err != nil {
			return fmt.Errorf("failed to inject Stop hook: %w", err)
		}
	}

	// Auto-register workspace
	if workspacePath == "" {
		fmt.Fprintln(os.Stderr, "Could not determine workspace — skipping workspace registration.")
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
	// --- context lines (only emitted when values are known) ---
	workspaceLine := ""
	if workspacePath != "" {
		workspaceLine = fmt.Sprintf("Workspace: `%s`\n", workspacePath)
	}

	defaultServiceLine := ""
	if defaultService != "" {
		defaultServiceLine = fmt.Sprintf("Default service: `%s` — MCP tools that accept `name` use this when `name` is omitted.\n", defaultService)
	}

	stopHookLine := ""
	if defaultService != "" {
		stopHookLine = fmt.Sprintf("\n> Note: a Stop hook is configured to call `devstack disable %s` when this Claude session ends.\n", defaultService)
	}

	devstackJsonPath := ".devstack.json"
	if workspacePath != "" {
		devstackJsonPath = workspacePath + "/.devstack.json"
	}

	tools := "" +
		"| Tool | Args | What it does |\n" +
		"|------|------|--------------|\n" +
		"| `status` | — | List all services with build status, runtime status, and ports. **Always call this first** — do not guess service names. |\n" +
		"| `start` | `name` (optional) | Tell Tilt to start/build a single service. Does not resolve dependencies — use `devstack enable` (CLI) if deps are needed. |\n" +
		"| `restart` | `name` (optional) | Rebuild and restart a service. Use after code changes. |\n" +
		"| `stop` | `name` (optional) | Stop a single service without touching others. |\n" +
		"| `start_all` | `services` (comma-separated, optional) | Start multiple services at once. Omit `services` to start everything. |\n" +
		"| `stop_all` | — | Stop all services. Tilt daemon keeps running. |\n" +
		"| `logs` | `name` (optional), `lines` (default 100) | Fetch recent log output from a service. |\n" +
		"| `errors` | `name` (optional), `lines` (default 50) | Fetch current error lines from a service — raw stderr/failure output. |\n" +
		"| `what_happened` | `name` (optional), `since_minutes` (default 15) | Get a chronological timeline of recent events: crashes, restarts, errors. Use this to diagnose *why* something broke, not just *what* is broken. |\n" +
		"| `set_environment` | `key`, `value` | Set a named Tilt argument, causing Tilt to reload affected services. Valid keys are declared in the Tiltfile via `config.parse` — grep the Tiltfile or ask the user what arg to set. Example: `key=ENV value=Staging` switches all .NET services to Staging. |\n"

	return "\n## Dev Stack (devstack MCP)\n\n" +
		"devstack is an MCP server that controls this workspace's services via Tilt (a local process orchestrator).\n" +
		workspaceLine +
		defaultServiceLine +
		"\n**First step**: always call `status` to see what's running and get exact service names before taking any action.\n\n" +
		"**If Tilt is not running**: call `devstack start` from the shell before using any MCP tools.\n" +
		stopHookLine + "\n" +
		"### MCP Tools\n\n" +
		tools + "\n" +
		"### Shell CLI\n\n" +
		"Use the shell CLI for lifecycle management and dependency-aware service control.\n" +
		"Prefer CLI over MCP tools when starting services that have dependencies.\n\n" +
		"| Command | What it does |\n" +
		"|---------|-------------|\n" +
		"| `devstack status` | Same as the MCP `status` tool — live service table with ports |\n" +
		"| `devstack enable <service>` | Start a service **and all its declared dependencies** (reads `.devstack.json`) |\n" +
		"| `devstack enable --group=<name>` | Start a named group of services with dep resolution |\n" +
		"| `devstack disable <service>` | Stop one service; leaves other services running |\n" +
		"| `devstack start` | Start the Tilt daemon for this workspace (required before MCP tools work) |\n" +
		"| `devstack down` | Stop the Tilt daemon — **this breaks all MCP tools until `devstack start` is run again** |\n" +
		"| `devstack open` | Open the Tilt UI in a browser |\n" +
		"| `devstack deps show` | Show declared service dependencies |\n" +
		"| `devstack deps add <svc> <dep>` | Declare that `<svc>` depends on `<dep>` |\n\n" +
		"### Service Dependencies\n\n" +
		"Dependencies are declared in `" + devstackJsonPath + "`. When you run `devstack enable <service>`, all deps are started first.\n\n" +
		"Format:\n" +
		"```json\n" +
		"{\n" +
		"  \"deps\": {\n" +
		"    \"service-a\": [\"service-b\", \"service-c\"],\n" +
		"    \"service-d\": [\"service-b\"]\n" +
		"  },\n" +
		"  \"groups\": {\n" +
		"    \"backend\": [\"service-b\", \"service-c\", \"service-a\"]\n" +
		"  }\n" +
		"}\n" +
		"```\n\n" +
		"If you determine that a service is missing a dependency (e.g. it fails to connect on startup), **confirm with the user** before editing `.devstack.json` — this file is shared and committed to the repo.\n"
}
