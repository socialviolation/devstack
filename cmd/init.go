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

	// Read existing content (if any), strip any previous devstack section, then re-append.
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

	instructions := buildInstructions(defaultService, workspacePath)
	if err := os.WriteFile(agentsFile, []byte(stripped+instructions), 0644); err != nil {
		return fmt.Errorf("failed to write AGENTS.md: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ devstack MCP instructions written to %s\n", agentsFile)

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

	// Start Jaeger OTEL container (non-fatal — a missing docker or network
	// must not block workspace init).
	containerName := workspace.OtelContainerName(ws.Name)
	if isOtelRunning(containerName) {
		fmt.Fprintf(os.Stderr, "Jaeger already running — %s\n", otelUIURL)
	} else {
		fmt.Fprintf(os.Stderr, "Starting Jaeger...")
		if err := startOtel(containerName); err != nil {
			fmt.Fprintf(os.Stderr, " failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "OTEL backend can be started manually:\n  docker run -d --name %s --restart unless-stopped -p 16686:16686 -p 4317:4317 -p 4318:4318 -e COLLECTOR_OTLP_ENABLED=true %s\n", containerName, otelImage)
		} else {
			fmt.Fprintf(os.Stderr, " started\n✓ Jaeger running — %s\n", otelUIURL)
		}
	}

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

	// Remove any existing devstack stop hook (replace, not append)
	filtered := hooksMap["Stop"][:0]
	replaced := false
	for _, entry := range hooksMap["Stop"] {
		isDevstackStop := false
		for _, h := range entry.Hooks {
			if strings.HasPrefix(h.Command, "devstack stop --default-service=") {
				isDevstackStop = true
				break
			}
		}
		if isDevstackStop {
			replaced = true
			continue // drop old entry
		}
		filtered = append(filtered, entry)
	}
	hooksMap["Stop"] = filtered

	if replaced {
		fmt.Fprintln(os.Stderr, "Replacing existing devstack Stop hook.")
	}

	// Append updated Stop hook entry
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
		contextLine += "\n"
	}

	stopHookLine := ""
	if defaultService != "" {
		stopHookLine = fmt.Sprintf("> Stop hook: `devstack stop %s` runs when this session ends.\n\n", defaultService)
	}

	devstackJsonPath := ".devstack.json"
	if workspacePath != "" {
		devstackJsonPath = workspacePath + "/.devstack.json"
	}

	startCmd := "devstack start <service>"
	if defaultService != "" {
		startCmd = "devstack start " + defaultService
	}

	return "\n## Dev Stack (devstack MCP)\n\n" +
		contextLine +
		stopHookLine +
		"### Quick Reference\n\n" +
		"```bash\n" +
		"# Starting\n" +
		"devstack status                  # is Tilt running?\n" +
		"devstack up                      # start Tilt if stopped\n" +
		"devstack services                # discover services, groups, deps\n" +
		"devstack start --group=<name>    # start group with dep resolution\n" +
		"\n" +
		"# Something broken (Tilt must be running — use MCP tools)\n" +
		"what_happened                    # always start here — traces + logs correlated\n" +
		"errors / logs                    # drill into a specific service\n" +
		"traces → trace_detail            # find and inspect a specific trace\n" +
		"\n" +
		"# Ending session\n" +
		"devstack stop " + defaultService + "        # stop only what you started\n" +
		"devstack status                  # verify\n" +
		"```\n\n" +
		"### Rules\n\n" +
		"- **CLI for lifecycle**: `up`, `down`, `start`, `stop`, `services`, `groups`, `deps`\n" +
		"- **MCP for observation**: `status`, `restart`, `stop`, `logs`, `errors`, `what_happened`, `traces`, `set_environment`\n" +
		"- **Never use MCP to spin up** — no dep resolution, requires Tilt already running\n" +
		"- **`what_happened` first** when something is broken — before `errors` or `logs`\n" +
		"- **Stop only what you started** — don't tear down the whole stack unless asked\n\n" +
		"### Commands\n\n" +
		"| Task | Command | Interface |\n" +
		"|------|---------|----------|\n" +
		"| Check Tilt is running | `devstack status` | CLI |\n" +
		"| Discover services, groups, deps | `devstack services` | CLI |\n" +
		"| Start Tilt daemon | `devstack up` | CLI |\n" +
		"| Start group with dep resolution | `devstack start --group=<name>` | CLI |\n" +
		"| Start service + its deps | `" + startCmd + "` | CLI |\n" +
		"| Stop one service | `devstack stop <service>` | CLI |\n" +
		"| Kill Tilt entirely | `devstack down` | CLI — destructive, only if asked |\n" +
		"| Live service state | `status` | MCP |\n" +
		"| Rebuild after code change | `restart [name]` | MCP |\n" +
		"| Stop a single service | `stop [name]` | MCP |\n" +
		"| First stop when broken | `what_happened [name]` | MCP |\n" +
		"| Scan for errors | `errors [name]` | MCP |\n" +
		"| Raw log output | `logs [name]` | MCP |\n" +
		"| Browse recent traces | `traces [service]` | MCP |\n" +
		"| Full span tree for a trace | `trace_detail <trace_id>` | MCP |\n" +
		"| Find trace by business attribute | `trace_search <attr> <value>` | MCP |\n" +
		"| Change Tilt config | `set_environment <key> <value>` | MCP |\n" +
		"\n" +
		"### Service Dependencies\n\n" +
		"Deps are declared in `" + devstackJsonPath + "`. `devstack start` reads them and starts deps first.\n\n" +
		"```bash\n" +
		"devstack deps add <svc> <dep>    # declare a dependency\n" +
		"devstack deps show <svc>         # verify resolved start order\n" +
		"```\n\n" +
		"Only add deps when a service consistently fails because another isn't running. **Confirm before adding** — shared config.\n"
}
