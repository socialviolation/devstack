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
		"| `what_happened` | `name` (optional), `since_minutes` (default 15) | **Start here when something is broken.** Correlates Jaeger traces + Tilt logs in one view: shows error trace count, failing operations, business attributes (portfolio.id, user.id), error messages, and raw log error lines. Degrades gracefully if Jaeger is not running. |\n" +
		"| `traces` | `service` (optional), `limit` (default 20), `since_minutes` (default 30) | List recent traces from Jaeger — timestamp, trace ID, operation, service, duration, ok/error. Use to see recent request activity. |\n" +
		"| `trace_detail` | `trace_id` (required) | Full span tree for a trace: every span with service, operation, duration, status, and business attributes. Use after finding a trace_id from `traces` or `trace_search`. |\n" +
		"| `trace_search` | `attribute` (required), `value` (required), `service` (optional), `limit` (default 10), `since_minutes` (default 60) | Find traces by business attribute — e.g. `attribute=portfolio.id value=123`. Searches one or all services. Use when a user reports a broken import or request. |\n" +
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
		"| `devstack deps show` | Show declared service dependencies |\n" +
		"| `devstack deps add <svc> <dep>` | Declare that `<svc>` depends on `<dep>` |\n" +
		"> Jaeger (http://localhost:16686) receives traces from all instrumented services. Use MCP `traces`/`trace_search`/`trace_detail` tools to query by service, trace ID, or business attributes.\n\n" +
		"### Service Dependencies\n\n" +
		"Dependencies are declared in `" + devstackJsonPath + "`. When you run `devstack enable <service>`, devstack reads this file and starts all deps first, in order.\n\n" +
		"**How to add a dependency**\n\n" +
		"Use the CLI — do not hand-edit the JSON:\n\n" +
		"```\n" +
		"devstack deps add <service> <dependency>\n" +
		"```\n\n" +
		"Example: you are working on `service-a` and it fails to connect because `service-b` is not running:\n\n" +
		"```\n" +
		"devstack deps add service-a service-b   # declare the dependency\n" +
		"devstack deps show service-a            # verify: shows resolved start order\n" +
		"devstack enable service-a              # now starts service-b first, then service-a\n" +
		"```\n\n" +
		"**When to add a dependency**\n\n" +
		"Add a dep when a service consistently fails to start because another service is not running — e.g. a connection refused error on startup pointing at another service in this workspace. Do not add deps speculatively.\n\n" +
		"**Confirm before adding** — `.devstack.json` is committed to the repo and shared. Ask the user before running `devstack deps add` if you are not certain the dependency is real.\n\n" +
		"**Check existing deps first**\n\n" +
		"```\n" +
		"devstack deps show              # all declared deps\n" +
		"devstack deps show <service>    # resolved start order for one service\n" +
		"```\n\n" +
		"### Adding New Services\n\n" +
		"To add a new service to this workspace, run from the workspace root:\n\n" +
		"```\n" +
		"devstack onboard <service-name> <service-path>\n" +
		"```\n\n" +
		"This will:\n" +
		"1. Auto-detect the service language (dotnet, go, python, node) from files in `<service-path>`\n" +
		"2. Append a `local_resource(...)` block to the workspace Tiltfile\n" +
		"3. Register the service path in `.devstack.json`\n" +
		"4. Write `.mcp.json` into the service directory (wires up devstack MCP)\n" +
		"5. Append devstack instructions to `AGENTS.md` in the service directory\n\n" +
		"Options:\n" +
		"```\n" +
		"devstack onboard <name> <path> --port=<port>    # specify HTTP port for readiness probe\n" +
		"devstack onboard <name> <path> --lang=<lang>    # override language detection\n" +
		"devstack onboard <name> <path> --label=<label>  # override Tiltfile label\n" +
		"```\n"
}
