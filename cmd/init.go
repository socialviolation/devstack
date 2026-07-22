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

const (
	agentsSentinelBegin = "<!-- devstack:begin — generated by `devstack init`; do not edit between these markers -->"
	agentsSentinelEnd   = "<!-- devstack:end -->"
	legacyAgentsHeader  = "## Dev Stack (devstack MCP)"
)

// writeAgentsMD writes the managed devstack block into AGENTS.md non-destructively.
// If a sentinel-wrapped block exists it is replaced in place; otherwise any legacy
// section is migrated away and a fresh block is appended, preserving all other content.
func writeAgentsMD(serviceName, servicePath, workspacePath string) error {
	agentsFile := filepath.Join(servicePath, "AGENTS.md")
	existing, err := os.ReadFile(agentsFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	block := agentsSentinelBegin + "\n" + buildAgentInstructions(serviceName, workspacePath) + agentsSentinelEnd
	updated := replaceManagedBlock(string(existing), block)
	return os.WriteFile(agentsFile, []byte(updated), 0644)
}

// replaceManagedBlock returns existing with the managed block set to block. An
// existing sentinel-wrapped block is replaced in place; otherwise a legacy
// devstack section is stripped and the block is appended at EOF. Content before
// and after the managed block is preserved; the result always ends in one newline
// and is idempotent (running twice yields byte-identical output).
func replaceManagedBlock(existing, block string) string {
	if begin := strings.Index(existing, agentsSentinelBegin); begin != -1 {
		if rel := strings.Index(existing[begin:], agentsSentinelEnd); rel != -1 {
			end := begin + rel + len(agentsSentinelEnd)
			return assembleAgents(existing[:begin], block, existing[end:])
		}
	}
	return assembleAgents(stripLegacyAgentsSection(existing), block, "")
}

// stripLegacyAgentsSection removes a legacy "## Dev Stack (devstack MCP)" section,
// from that header up to (but not including) the next "## " header at column 0, or
// EOF if none — so a following section such as "## BEADS" survives.
func stripLegacyAgentsSection(s string) string {
	start := -1
	if strings.HasPrefix(s, legacyAgentsHeader) {
		start = 0
	} else if i := strings.Index(s, "\n"+legacyAgentsHeader); i != -1 {
		start = i + 1
	}
	if start == -1 {
		return s
	}
	rest := s[start+len(legacyAgentsHeader):]
	if i := strings.Index(rest, "\n## "); i != -1 {
		return s[:start] + rest[i+1:]
	}
	return s[:start]
}

func assembleAgents(before, block, after string) string {
	before = strings.TrimRight(before, "\n")
	after = strings.Trim(after, "\n")
	var sb strings.Builder
	if before != "" {
		sb.WriteString(before)
		sb.WriteString("\n\n")
	}
	sb.WriteString(block)
	if after != "" {
		sb.WriteString("\n\n")
		sb.WriteString(after)
	}
	sb.WriteString("\n")
	return sb.String()
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

	svc := "<service>"
	if defaultService != "" {
		svc = defaultService
	}

	observabilityBlock := "**Observability:** Not enabled for this workspace — services are not assumed to be OTEL-instrumented and no collector runs. " +
		"Turn it on with `devstack otel enable`, then `devstack otel start`.\n\n"
	if config.ObservabilityEnabled(workspacePath) {
		observabilityBlock = "**Observability:** Services ship traces and logs to the local collector (gRPC `localhost:4317`). " +
			"Every signal is tagged with `devstack.workspace`, `devstack.service`, and `devstack.stack` resource attributes, " +
			"so logs and traces can be isolated to one stack's instance of a service. " +
			"Route telemetry upstream with `devstack otel configure`; open the UI with `devstack otel open`. " +
			"Per-developer endpoint override: set `OTEL_EXPORTER_OTLP_ENDPOINT` in `.envrc`.\n\n"
	}

	return "## Dev Stack (devstack MCP)\n\n" +
		"devstack is a CLI and MCP server that gives agents programmatic control over a local development stack. " +
		"It runs services on top of [Tilt](https://tilt.dev), handling lifecycle, dependency ordering, and observability. " +
		"Local dev only — never point devstack at staging or production.\n\n" +
		contextLine +
		"### One host daemon\n\n" +
		"devstack runs a single Tilt daemon for the whole machine (port 10300). " +
		"Every active workspace's services run inside that one daemon, alongside the services of every active feature stack. " +
		"There is no daemon per workspace and none per stack — one daemon holds them all.\n\n" +
		"### Which instance am I operating on\n\n" +
		"Every running service is a Tilt resource whose name tells you which instance you are touching:\n\n" +
		"- Base service: `<workspace>:<service>` (e.g. `" + wsBaseName(workspacePath) + ":" + svc + "`).\n" +
		"- A feature stack's copy of that service: `<workspace>:<service>:<stack>`.\n\n" +
		"The base instance and each stack's instance listen on **different ports**, and every command names which instance it acted on (a `target:` line naming the stack, blank for base) — so ports plus that target line tell you which running copy you are inspecting or controlling.\n\n" +
		"### Feature stacks\n\n" +
		"A feature stack is a parallel version of one or more services, run from a git worktree on a feature branch, " +
		"on its own dynamically-allocated port, beside the base — reusing the base stack for every service it does not change. " +
		"Two stacks means two worktrees, two branches, and two extra ports, all live at once in the one host daemon. " +
		"A shared database is **not** isolated per stack — stacks write to the same DB as base unless the service itself points elsewhere.\n\n" +
		"### Working on a stack (branch + worktree)\n\n" +
		"Each stack's changed service lives in its **own git worktree** — a separate folder checked out on the stack's branch, created by `devstack stack create` (path shown in its output and by `devstack stack list`). To work on the feature, **`cd` into that worktree folder and edit there.** Do NOT `git checkout` the feature branch in the base checkout: a stack is *already* its own folder on its own branch, so you switch context by changing directory, never by switching branches. Edits in a stack's worktree stay on that stack's branch; base and every other stack are untouched. After editing, reload only that instance with `devstack restart " + svc + " --stack <name>` (or `devstack start " + svc + " --stack <name>`), then verify against that instance's own port.\n\n" +
		"### Finishing a stack (clean up as you go)\n\n" +
		"When a feature's work is done, close its stack out — do not let inactive stacks, worktrees, and branches pile up (stale stack config is a liability). Steps:\n\n" +
		"1. Commit the work on the stack's branch, inside its worktree.\n" +
		"2. **Ask the human** whether to merge/combine the branch into base (or open a PR) or discard it — never merge unilaterally.\n" +
		"3. If merging: merge the stack's branch into the base branch, then `git branch -d <branch>` to prune it (`stack rm` does NOT delete the branch).\n" +
		"4. `devstack stack rm <name>` — stops the stack, removes its worktree(s), releases its ports, and deletes its config/record. It refuses if a worktree has uncommitted changes; commit or discard first (`--force` discards).\n" +
		"5. Periodically run `devstack stack list` and prune stacks whose work has landed or been abandoned.\n\n" +
		"### Commands\n\n" +
		"```bash\n" +
		"devstack status                              # live state of every instance (add --stack <name> for one stack)\n" +
		"devstack topology                            # services, groups, deps, dependents\n" +
		"devstack workspace doctor                    # check workspace manifests and topology integrity\n" +
		"devstack otel status                         # collector state + per-service telemetry evidence\n" +
		"devstack workspace up                        # start the host daemon + this workspace's services\n" +
		"devstack workspace down                      # stop this workspace's services\n" +
		"devstack start " + svc + "                          # start this service + its dependencies\n" +
		"devstack restart " + svc + " [--stack <name>]       # restart base, or the stack's instance\n" +
		"devstack stop " + svc + " [--stack <name>]          # stop base, or the stack's instance\n" +
		"devstack stack create <name> --repos " + svc + "    # new feature stack overlaying the base\n" +
		"devstack stack up <name>                     # bring the stack's services up on their own ports\n" +
		"devstack stack down <name>                   # stop the stack (keeps its worktrees)\n" +
		"devstack stack rm <name>                     # tear down: remove worktrees, release ports, delete config\n" +
		"devstack stack list                          # registered stacks and their ports\n" +
		"devstack stack config " + svc + " --stack <name>    # effective config a stack's service runs with\n" +
		"devstack tunnel push [--stacks]              # forward local service ports over SSH (--stacks includes stack instances)\n" +
		"devstack env set <name> KEY=VALUE            # define an environment's config-patch values\n" +
		"devstack env use <name> [--service|--stack]  # point base, a service, or a stack at env <name>\n" +
		"devstack env which [--service|--stack]        # which env an instance resolves to, and its values\n" +
		"```\n\n" +
		"`--stack <name>` targets that stack's instance instead of base; without it commands operate on the base workspace.\n\n" +
		"### Environments (where a service points)\n\n" +
		"An **environment** (`environments:` in the workspace manifest) is a named bundle of config-var patches — DB URLs, feature flags, external endpoints — that repoints services without code changes. It applies at three scopes, most-specific winning: a **stack**'s env beats a **service**'s env beats the **workspace** default. So base can run against `local` while one stack runs against `prod`. `devstack status` shows each instance's active env (the ENV column / `env:<name>`), so you can see where every running copy is pointed. Set values with `devstack env set`, point a scope with `devstack env use`. Do NOT `env set` real secrets — those values land in the committed manifest; keep secrets in `env.required` + `.envrc` and let envs carry only non-secret pointing config.\n\n" +
		"### MCP tools\n\n" +
		"The `.mcp.json` in this repo wires up the devstack MCP server — the agent interface. Tools include " +
		"`status`, `restart`, `stop`, `configure`, `process_logs`, `investigate`, and `environment`, plus stack tools " +
		"(`stack_create`, `stack_list`, `stack_rm`). The service-control tools (`status`, `restart`, `stop`, `process_logs`, `configure`) " +
		"take an optional `stack` parameter to target a stack's instance rather than base (omit it, or pass `\"base\"`, for base); " +
		"`investigate` also takes `stack` but as a telemetry filter. Treat them as discovery helpers, not hidden sources of truth.\n\n" +
		"Rules:\n" +
		"1. Check `topology` before making dependency claims.\n" +
		"2. Prefer process logs and telemetry evidence over guessing about runtime state.\n" +
		"3. When telemetry is partial or inconclusive, fall back to process logs and live status.\n" +
		"4. Do not use devstack against staging or production.\n\n" +
		observabilityBlock
}

func wsBaseName(workspacePath string) string {
	if workspacePath == "" {
		return "<workspace>"
	}
	return filepath.Base(workspacePath)
}
