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

	"devstack/internal/config"
	"devstack/internal/workspace"
)

var initAll bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Append dev stack MCP instructions to AGENTS.md",
	Long:  `Appends LLM instructions for the devstack MCP tools to AGENTS.md in the current directory. Use --all to update every service in the workspace.`,
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initAll, "all", false, "Update AGENTS.md for every service registered in the workspace")
}

func runInit(cmd *cobra.Command, args []string) error {
	if initAll {
		return runInitAll()
	}

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

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	if err := writeAgentsMD(defaultService, cwd, workspacePath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ devstack MCP instructions written to AGENTS.md\n")

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
	hookCommand := fmt.Sprintf("devstack tilt stop --default-service=%s --if-last-session --workspace=%s", defaultService, workspacePath)

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

func runInitAll() error {
	ws, err := workspace.DetectFromCwd()
	if err != nil {
		return fmt.Errorf("could not detect workspace from current directory: %w\nRun from within a registered workspace, or use 'devstack init' in a specific service directory.", err)
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return fmt.Errorf("failed to load workspace config: %w", err)
	}

	if len(cfg.ServicePaths) == 0 {
		fmt.Fprintln(os.Stderr, "No services registered in this workspace. Use 'devstack onboard' to add services.")
		return nil
	}

	// Sort for deterministic output
	services := make([]string, 0, len(cfg.ServicePaths))
	for name := range cfg.ServicePaths {
		services = append(services, name)
	}
	sort.Strings(services)

	fmt.Fprintf(os.Stderr, "Updating AGENTS.md for %d services in workspace '%s'\n", len(services), ws.Name)

	var errs []string
	for _, name := range services {
		svcPath := cfg.ServicePaths[name]
		if err := writeAgentsMD(name, svcPath, ws.Path); err != nil {
			errs = append(errs, fmt.Sprintf("  %s: %v", name, err))
			fmt.Fprintf(os.Stderr, "✗ %s (%s): %v\n", name, svcPath, err)
		} else {
			fmt.Fprintf(os.Stderr, "✓ %s (%s)\n", name, svcPath)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d service(s) failed:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return nil
}

// writeAgentsMD strips and rewrites the devstack section of AGENTS.md for a service.
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

	instructions := buildInstructions(serviceName, workspacePath)
	if err := os.WriteFile(agentsFile, []byte(stripped+instructions), 0644); err != nil {
		return fmt.Errorf("failed to write AGENTS.md: %w", err)
	}
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

	startCmd := "devstack start <service>"
	if defaultService != "" {
		startCmd = "devstack start " + defaultService
	}

	return "\n## Dev Stack (devstack MCP)\n\n" +
		"> **LOCAL DEV ONLY** — devstack manages local services running under Tilt.\n" +
		"> Do not use it to investigate staging or production issues.\n\n" +
		contextLine +
		"### Starting the stack (CLI)\n\n" +
		"MCP tools require Tilt already running. Always use the shell CLI to spin up.\n\n" +
		"```bash\n" +
		"devstack status                         # check all workspaces\n" +
		"devstack tilt up                        # start Tilt if stopped\n" +
		"devstack tilt status                    # list services, groups, and status\n" +
		"devstack tilt start --group=<group>     # start a named group (resolves deps)\n" +
		startCmd + "                            # start this service + its deps\n" +
		"```\n\n" +
		"### While the stack is running (MCP tools)\n\n" +
		"| Need | Tool |\n" +
		"|------|------|\n" +
		"| Live service states + ports | `status` |\n" +
		"| Something is broken — start here | `investigate` — traces + correlated logs in one call |\n" +
		"| Raw stdout/stderr | `process_logs` (set `errors_only=true` to filter noise) |\n" +
		"| Rebuild after a code change | `restart [name]` |\n" +
		"| Stop service(s) | `stop [name]` — omit name to stop all |\n" +
		"| Change a Tilt config value | `configure key=<k> value=<v>` |\n\n" +
		"### Rules\n\n" +
		"- **`investigate` first** when something is broken — it correlates traces and logs in one call\n" +
		"- **Stop only what you started** — don't tear down the whole stack unless asked\n" +
		"- **Never use devstack for prod/staging** — it only sees local Tilt-managed processes\n"
}
