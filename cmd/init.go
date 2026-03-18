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
		stopHookLine = fmt.Sprintf("\n> Note: a Stop hook is configured to call `devstack stop %s` when this Claude session ends.\n", defaultService)
	}

	devstackJsonPath := ".devstack.json"
	if workspacePath != "" {
		devstackJsonPath = workspacePath + "/.devstack.json"
	}

	tools := "" +
		"| Tool | Args | What it does |\n" +
		"|------|------|--------------|\n" +
		"| `status` | — | List all services with STATUS (idle/starting/running/error) and PORT(S). **Always call this first** — do not guess service names or assume what's running. |\n" +
		"| `start` | `name` (optional) | Tell Tilt to start/build a single service. Auto-enables disabled services. Does not resolve dependencies — use `devstack start` (CLI) if deps are needed. |\n" +
		"| `restart` | `name` (optional) | Rebuild and restart a service. Auto-enables if disabled. Use after code changes. |\n" +
		"| `stop` | `name` (optional) | Stop a single service without touching others. |\n" +
		"| `start_all` | `services` (comma-separated, optional) | Start all non-disabled services, or a specific subset. Does not resolve dependencies — use `devstack start --group` for dep-aware startup. |\n" +
		"| `stop_all` | — | Stop all services. Tilt daemon keeps running. |\n" +
		"| `logs` | `name` (optional), `lines` (default 100) | Fetch recent log output from a service. |\n" +
		"| `errors` | `name` (optional), `lines` (default 50) | Fetch error lines from a service (or all services if name omitted). Use for a quick scan before calling `what_happened`. |\n" +
		"| `what_happened` | `name` (optional), `since_minutes` (default 15) | **Start here when something is broken.** Correlates Jaeger traces + Tilt logs in one view: shows error trace count, failing operations, business attributes (portfolio.id, user.id), error messages, and raw log error lines. Degrades gracefully if Jaeger is not running. |\n" +
		"| `traces` | `service` (optional), `limit` (default 20), `since_minutes` (default 30) | List recent traces from Jaeger — timestamp, trace ID, operation, service, duration, ok/error. Use after `what_happened` to browse recent activity. |\n" +
		"| `trace_detail` | `trace_id` (required) | Full span tree for a trace: every span with service, operation, duration, status, and business attributes. Use after finding a trace_id from `traces` or `trace_search`. |\n" +
		"| `trace_search` | `attribute` (required), `value` (required), `service` (optional), `limit` (default 10), `since_minutes` (default 60) | Find traces by business attribute — e.g. `attribute=portfolio.id value=123`. Use when a user reports a broken import or request by ID. |\n" +
		"| `set_environment` | `key`, `value` | Set a named Tilt argument, causing Tilt to reload affected services. Valid keys are declared in the Tiltfile via `config.parse` — grep the Tiltfile or ask the user what arg to set. Example: `key=ENV value=Staging` switches all .NET services to Staging. |\n"

	tearDown := "### Tearing Down the Dev Stack\n\n" +
		"When ending a session, stop only the services you started — do not tear down the whole stack unless asked.\n\n" +
		"```\n" +
		"devstack stop " + defaultService + "          # stop just this service\n" +
		"devstack status                            # verify it stopped\n" +
		"```\n\n" +
		"If you started a group, stop each service individually — deps are not auto-stopped.\n\n" +
		"Full stack teardown (only if explicitly asked):\n\n" +
		"```\n" +
		"devstack down                              # kills Tilt and all running services\n" +
		"```\n\n"

	return "\n## Dev Stack (devstack MCP)\n\n" +
		"devstack is an MCP server that controls this workspace's services via Tilt (a local process orchestrator).\n" +
		workspaceLine +
		defaultServiceLine +
		stopHookLine + "\n" +
		"### Spinning up the dev stack\n\n" +
		"The MCP `status` tool and `start`/`start_all` tools **require Tilt to already be running**. Always use the shell CLI to spin up.\n\n" +
		"```\n" +
		"1. devstack status                         # CLI: check if Tilt is running\n" +
		"                                           #   if output says 'stopped' → go to step 2\n" +
		"                                           #   if services show STATUS 'idle' or 'disabled' → Tilt is running,\n" +
		"                                           #   services just haven't been started yet → go to step 3\n" +
		"2. devstack up                             # start Tilt daemon (only if stopped)\n" +
		"3. devstack services                       # discover groups and deps for all services\n" +
		"   devstack groups find " + defaultService + "   # or: find groups for just this service\n" +
		"4. devstack start --group=<name>           # start that group (resolves deps, starts in order)\n" +
		"```\n\n" +
		"Start the group associated with the current service — **not all services**. If multiple groups are returned by `groups find`, pick the smallest one that covers what the user needs, or ask.\n\n" +
		"If no group exists for this service, use `devstack start " + defaultService + "` to start it and its declared dependencies only.\n\n" +
		"**Do not use the MCP `start` or `start_all` tools to spin up services** — they do not resolve dependencies and fail if Tilt is not yet running. Always use `devstack start` from the shell.\n\n" +
		"### MCP Tools\n\n" +
		tools + "\n" +
		"### Shell CLI\n\n" +
		"Use the shell CLI for lifecycle management and dependency-aware service control.\n" +
		"Prefer CLI over MCP tools when starting services that have dependencies.\n\n" +
		"| Command | What it does |\n" +
		"|---------|-------------|\n" +
		"| `devstack status` | Show per-service status for the current workspace — build/runtime state and ports |\n" +
		"| `devstack services` | Show all known services with runtime status, group membership, and declared deps — use this to discover what's available |\n" +
		"| `devstack start <service>` | Start a service **and all its declared dependencies** (reads `.devstack.json`). Auto-enables disabled services. |\n" +
		"| `devstack start --group=<name>` | Start a named group of services with dep resolution |\n" +
		"| `devstack stop <service>` | Stop one service; leaves other services running |\n" +
		"| `devstack up` | Start the Tilt daemon for this workspace (required before MCP tools work) |\n" +
		"| `devstack down` | Stop the Tilt daemon — **this breaks all MCP tools until `devstack up` is run again** |\n" +
		"| `devstack groups find <service>` | Show which groups contain a service — use this to find the right group to enable |\n" +
		"| `devstack groups list` | List all declared groups and their members |\n" +
		"| `devstack groups add <group> <svc> [svc...]` | Add services to a group (creates it if it doesn't exist) |\n" +
		"| `devstack groups remove <group> <svc> [svc...]` | Remove services from a group |\n" +
		"| `devstack deps show [service]` | Show declared deps for all services, or resolved start order for one service |\n" +
		"| `devstack deps add <svc> <dep>` | Declare that `<svc>` depends on `<dep>` |\n" +
		"| `devstack deps remove <svc> <dep>` | Remove a declared dependency |\n" +
		"\n" +
		"> Jaeger (http://localhost:16686) receives traces from all instrumented services. Use MCP `traces`/`trace_search`/`trace_detail` tools to query by service, trace ID, or business attributes.\n\n" +
		"### Service Dependencies\n\n" +
		"Dependencies are declared in `" + devstackJsonPath + "`. When you run `devstack start <service>`, devstack reads this file and starts all deps first, in order.\n\n" +
		"**How to add a dependency**\n\n" +
		"Use the CLI — do not hand-edit the JSON:\n\n" +
		"```\n" +
		"devstack deps add <service> <dependency>\n" +
		"```\n\n" +
		"Example: `service-a` fails to connect because `service-b` is not running:\n\n" +
		"```\n" +
		"devstack deps add service-a service-b   # declare the dependency\n" +
		"devstack deps show service-a            # verify: shows resolved start order\n" +
		"devstack start service-a               # now starts service-b first, then service-a\n" +
		"```\n\n" +
		"Add a dep only when a service consistently fails to start because another service is not running. Do not add deps speculatively. **Confirm before adding** — `.devstack.json` is committed to the repo and shared.\n\n" +
		tearDown
}
