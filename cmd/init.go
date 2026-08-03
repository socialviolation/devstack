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
	"github.com/socialviolation/devstack/internal/stack"
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
    5. Regenerates the dev daemon config so 'devstack service start' can run it

  Use --force to overwrite an existing service manifest (for example to update the run command).

REFRESH ONLY (no --name/--path/--cmd flags)
  Re-writes the devstack section of AGENTS.md in the current service directory with
  the latest instructions. Use --all to refresh every service in the workspace at once.

  The refresh also removes devstack content that is now obsolete: a duplicate
  generated block, and a section from before the markers existed. It changes
  nothing outside the markers, so your own text survives.

SESSION BRIEFING (--claude-hook)
  Writes a Claude Code SessionStart hook into .claude/settings.json, so each
  session starts with the live briefing that 'devstack prime' prints. It merges
  into the file and keeps every other key and every hook you already declared.
  It is opt-in, because that file is committed and shared with your team.

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
  devstack init --all              # refresh AGENTS.md in every service
  devstack init --all --claude-hook  # also brief each Claude Code session`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().String("name", "", "Service name")
	initCmd.Flags().String("path", "", "Absolute path to the service directory")
	initCmd.Flags().String("cmd", "", "Command to run the service (for example \"go run .\" or \"dotnet run\")")
	initCmd.Flags().Int("port", 0, "HTTP port the service listens on (enables health checks and dashboard links)")
	initCmd.Flags().String("language", "", "Language override: dotnet, python, node, go (default: auto-detect)")
	initCmd.Flags().String("group", "", "Suggest a group for the service (add it with 'devstack group add')")
	initCmd.Flags().Bool("all", false, "Refresh AGENTS.md for every registered service in the workspace")
	initCmd.Flags().Bool("force", false, "Overwrite existing service configuration if it already exists")
	initCmd.Flags().Bool("claude-hook", false, "Also write the Claude Code SessionStart hook into .claude/settings.json, so every session is briefed by 'devstack prime'")
}

func runInit(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	name, _ := cmd.Flags().GetString("name")
	claudeHook, _ := cmd.Flags().GetBool("claude-hook")

	// --all: refresh AGENTS.md for every service
	if all {
		return runInitAll(claudeHook)
	}

	// No --name: refresh mode (AGENTS.md only)
	if name == "" {
		return runInitRefresh(cmd, claudeHook)
	}

	// Full onboard mode
	return runInitOnboard(cmd, claudeHook)
}

// applyClaudeHook writes the SessionStart hook into dir when the caller asked
// for it, and otherwise says the flag exists.
//
// It is opt-in on purpose. .claude/settings.json is committed and shared with a
// team, and a hook runs a command on every session of every person who clones
// the repo. devstack changes that file only when someone asks for it.
func applyClaudeHook(dir string, enabled bool) {
	if !enabled {
		return
	}
	changed, err := ensureClaudeSessionHook(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return
	}
	if changed {
		fmt.Fprintf(os.Stderr, "✓ %s briefs each session with 'devstack prime'\n", filepath.Join(dir, claudeSettingsRel))
	}
}

// claudeHookHint tells the reader the hook exists, once per run. A briefing that
// nobody knows to install is a briefing nobody gets.
func claudeHookHint(enabled bool) {
	if enabled {
		return
	}
	fmt.Fprintf(os.Stderr, "  To brief each Claude Code session automatically, run: devstack init --all --claude-hook\n")
}

// runInitRefresh rewrites the devstack section of AGENTS.md in the current directory.
func runInitRefresh(cmd *cobra.Command, claudeHook bool) error {
	defaultService := viper.GetString("default_service")
	workspacePath := viper.GetString("workspace")

	if workspacePath == "" {
		if ws, err := resolveWorkspace(""); err == nil {
			workspacePath = ws.Path
		}
	}

	if defaultService == "" {
		// The directory a service lives in is routinely not what the service is
		// called, and every command example in the generated file names it.
		if cwd, err := os.Getwd(); err == nil {
			if identity, ierr := config.ResolveIdentity(cwd); ierr == nil && identity.ServiceName != "" {
				defaultService = identity.ServiceName
			} else {
				defaultService = filepath.Base(cwd)
			}
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	if err := writeAgentsMD(defaultService, cwd, workspacePath, ""); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ AGENTS.md updated for service %q\n", defaultService)

	if err := refreshMCPJson(cwd, defaultService); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "✓ .mcp.json updated\n")
	}

	files, err := writeAIInstructionPointers(defaultService, cwd, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
	} else if len(files) > 0 {
		fmt.Fprintf(os.Stderr, "✓ %s updated\n", strings.Join(files, ", "))
	}
	applyClaudeHook(cwd, claudeHook)
	claudeHookHint(claudeHook)
	return nil
}

// runInitAll refreshes AGENTS.md for every service registered in the workspace.
func runInitAll(claudeHook bool) error {
	ws, err := resolveWorkspace("")
	if err != nil {
		return fmt.Errorf("can not detect workspace from current directory: %w\nRun from within a registered workspace.", err)
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

	fmt.Fprintf(os.Stderr, "Refreshing AGENTS.md and .mcp.json for %d services in workspace '%s'\n", len(services), ws.Name)

	var errs []string
	for _, svcName := range services {
		svcPath := cfg.ServicePaths[svcName]
		if err := writeAgentsMD(svcName, svcPath, ws.Path, ""); err != nil {
			errs = append(errs, fmt.Sprintf("  %s: %v", svcName, err))
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", svcName, err)
			continue
		}
		if err := refreshMCPJson(svcPath, svcName); err != nil {
			errs = append(errs, fmt.Sprintf("  %s: %v", svcName, err))
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", svcName, err)
			continue
		}
		files, err := writeAIInstructionPointers(svcName, svcPath, "")
		if err != nil {
			errs = append(errs, fmt.Sprintf("  %s: %v", svcName, err))
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", svcName, err)
			continue
		}
		applyClaudeHook(svcPath, claudeHook)
		fmt.Fprintf(os.Stderr, "✓ %s%s\n", svcName, agentFilesSuffix(append([]string{".mcp.json"}, files...)))
	}

	if files, err := writeAIInstructionPointers("", ws.Path, ""); err != nil {
		errs = append(errs, fmt.Sprintf("  workspace root: %v", err))
		fmt.Fprintf(os.Stderr, "✗ workspace root: %v\n", err)
	} else if len(files) > 0 {
		fmt.Fprintf(os.Stderr, "✓ workspace root%s\n", agentFilesSuffix(files))
	}

	applyClaudeHook(ws.Path, claudeHook)
	errs = append(errs, refreshStackAgentsMD(ws.Name, ws.Path, claudeHook)...)
	claudeHookHint(claudeHook)

	if len(errs) > 0 {
		return fmt.Errorf("%d service(s) failed:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return nil
}

// refreshStackAgentsMD rewrites AGENTS.md in every feature stack's worktree
// services, which live outside the base workspace's service paths and would
// otherwise go stale. It returns per-service failure lines rather than aborting,
// so one broken stack does not stop the rest.
func refreshStackAgentsMD(workspaceName, workspacePath string, claudeHook bool) []string {
	recs, err := stack.LoadStore(workspaceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: can not load stacks for workspace %q: %v\n", workspaceName, err)
		return nil
	}

	var errs []string
	for _, rec := range recs {
		rw, err := stack.ResolveWorktree(&rec)
		if err != nil {
			errs = append(errs, fmt.Sprintf("  stack %s: %v", rec.Name, err))
			fmt.Fprintf(os.Stderr, "✗ stack %s: %v\n", rec.Name, err)
			continue
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			svc := rw.Services[name]
			if err := writeAgentsMD(name, svc.RepoPath, workspacePath, rec.Name); err != nil {
				errs = append(errs, fmt.Sprintf("  %s (stack %s): %v", name, rec.Name, err))
				fmt.Fprintf(os.Stderr, "✗ %s (stack %s): %v\n", name, rec.Name, err)
				continue
			}
			if err := refreshMCPJson(svc.RepoPath, name); err != nil {
				errs = append(errs, fmt.Sprintf("  %s (stack %s): %v", name, rec.Name, err))
				fmt.Fprintf(os.Stderr, "✗ %s (stack %s): %v\n", name, rec.Name, err)
				continue
			}
			files, err := writeAIInstructionPointers(name, svc.RepoPath, rec.Name)
			if err != nil {
				errs = append(errs, fmt.Sprintf("  %s (stack %s): %v", name, rec.Name, err))
				fmt.Fprintf(os.Stderr, "✗ %s (stack %s): %v\n", name, rec.Name, err)
				continue
			}
			applyClaudeHook(svc.RepoPath, claudeHook)
			fmt.Fprintf(os.Stderr, "✓ %s (stack %s)%s\n", name, rec.Name, agentFilesSuffix(append([]string{".mcp.json"}, files...)))
		}
	}
	return errs
}

// runInitOnboard registers a new service and wires it up (full onboard).
func runInitOnboard(cmd *cobra.Command, claudeHook bool) error {
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
	if err := writeAgentsMD(name, path, ws.Path, ""); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write AGENTS.md: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "✓ Wrote AGENTS.md\n")
	}
	if files, err := writeAIInstructionPointers(name, path, ""); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	} else if len(files) > 0 {
		fmt.Fprintf(os.Stderr, "✓ Updated %s\n", strings.Join(files, ", "))
	}

	applyClaudeHook(path, claudeHook)

	// 5. Regenerate the Tiltfile so the daemon picks up the new service.
	if _, err := regenerateHostTiltfile(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to regenerate Tiltfile: %v\n", err)
	}

	// Summary
	fmt.Printf("\n✓ %q registered in workspace %q\n\n", name, ws.Name)
	fmt.Printf("  manifest:   %s\n", manifestPath)
	fmt.Printf("  .mcp.json:  %s\n", mcpFile)
	fmt.Printf("  AGENTS.md:  %s\n", filepath.Join(path, "AGENTS.md"))
	fmt.Printf("\nNext:\n")
	if group != "" {
		fmt.Printf("  devstack group add %s %s\n", group, name)
	}
	fmt.Printf("  devstack dependencies add %s <dep>   # declare dependencies\n", name)
	fmt.Printf("  devstack service start %s --stack base   # start it\n", name)

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

// refreshMCPJson rewrites a service's .mcp.json. The file is wholly generated, so
// it is overwritten rather than merged — that is what de-bakes older copies that
// still carry a machine-specific DEVSTACK_WORKSPACE / DEVSTACK_DAEMON_PORT.
func refreshMCPJson(servicePath, serviceName string) error {
	if err := writeMCPJson(filepath.Join(servicePath, ".mcp.json"), serviceName); err != nil {
		return fmt.Errorf("failed to write .mcp.json: %w", err)
	}
	return nil
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
	legacyPointerHeader = "## devstack (local dev services)"
)

// agentsProvenance stamps the generated block with the devstack that wrote it.
//
// This file is committed into every service repo, so without it there is no way
// to tell instructions a current devstack generated from instructions left by
// one several breaking changes ago — and the CLI they name has already been
// renamed once. It is a line inside the block, never part of the sentinel: the
// sentinel is matched by exact string, so versioning it would leave every file
// an older devstack wrote unmatchable, and the block would be appended again
// instead of replaced.
func agentsProvenance() string {
	return "<!-- devstack " + buildStamp() + " · regenerate with `devstack init --all` -->\n"
}

// writeAgentsMD writes the managed devstack block into AGENTS.md non-destructively.
// If a sentinel-wrapped block exists it is replaced in place; otherwise any legacy
// section is migrated away and a fresh block is appended, preserving all other content.
func writeAgentsMD(serviceName, servicePath, workspacePath, stackName string) error {
	agentsFile := filepath.Join(servicePath, "AGENTS.md")
	existing, err := os.ReadFile(agentsFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	block := agentsSentinelBegin + "\n" + agentsProvenance() + buildAgentInstructions(serviceName, servicePath, workspacePath, stackName) + agentsSentinelEnd
	updated := replaceManagedBlock(string(existing), block)
	return os.WriteFile(agentsFile, []byte(updated), 0644)
}

// replaceManagedBlock returns existing with the managed block set to block, and
// removes every other trace of a previous generation.
//
// The first sentinel-wrapped block is replaced where it stands. Any further
// sentinel-wrapped block is a duplicate an older devstack appended and is
// dropped, and so is a legacy unsentinelled devstack section wherever it sits.
// Everything outside those is a human's and is preserved. The result ends in one
// newline and is idempotent: running twice yields byte-identical output.
func replaceManagedBlock(existing, block string) string {
	before, after, found := cutFirstManagedBlock(existing)
	if !found {
		return assembleAgents(stripLegacySections(existing, legacyAgentsHeader), block, "")
	}
	return assembleAgents(
		stripLegacySections(before, legacyAgentsHeader),
		block,
		stripLegacySections(dropManagedBlocks(after), legacyAgentsHeader),
	)
}

// cutFirstManagedBlock splits s around the first complete sentinel-wrapped block.
func cutFirstManagedBlock(s string) (before, after string, found bool) {
	begin := strings.Index(s, agentsSentinelBegin)
	if begin == -1 {
		return s, "", false
	}
	rel := strings.Index(s[begin:], agentsSentinelEnd)
	if rel == -1 {
		return s, "", false
	}
	end := begin + rel + len(agentsSentinelEnd)
	return s[:begin], s[end:], true
}

// dropManagedBlocks removes every complete sentinel-wrapped block from s.
func dropManagedBlocks(s string) string {
	for {
		before, after, found := cutFirstManagedBlock(s)
		if !found {
			return s
		}
		s = strings.TrimRight(before, "\n") + "\n" + strings.TrimLeft(after, "\n")
	}
}

// stripLegacySections removes every unsentinelled devstack section named by
// header, from that header up to (but not including) the next "## " header at
// column 0, or EOF if none — so a following section such as "## BEADS" survives.
func stripLegacySections(s, header string) string {
	for {
		stripped, removed := stripOneLegacySection(s, header)
		if !removed {
			return s
		}
		s = stripped
	}
}

func stripOneLegacySection(s, header string) (string, bool) {
	start := -1
	if strings.HasPrefix(s, header) {
		start = 0
	} else if i := strings.Index(s, "\n"+header); i != -1 {
		start = i + 1
	}
	if start == -1 {
		return s, false
	}
	rest := s[start+len(header):]
	if i := strings.Index(rest, "\n## "); i != -1 {
		return s[:start] + rest[i+1:], true
	}
	return s[:start], true
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

// aiInstructionFiles are the instruction files coding agents read.
// devstack updates the ones a repo already has and never creates any of them.
var aiInstructionFiles = []string{
	"CLAUDE.md",
	"GEMINI.md",
	".cursorrules",
	filepath.Join(".github", "copilot-instructions.md"),
}

// writeAIInstructionPointers refreshes the managed devstack block in whichever
// aiInstructionFiles already exist under dir, and returns the ones it updated.
func writeAIInstructionPointers(serviceName, dir, stackName string) ([]string, error) {
	block := agentsSentinelBegin + "\n" + buildAIInstructionPointer(serviceName, stackName) + agentsSentinelEnd
	var updated []string
	for _, rel := range aiInstructionFiles {
		path := filepath.Join(dir, rel)
		existing, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return updated, fmt.Errorf("failed to read %s: %w", rel, err)
		}
		if err := os.WriteFile(path, []byte(replacePointerBlock(string(existing), block)), 0644); err != nil {
			return updated, fmt.Errorf("failed to write %s: %w", rel, err)
		}
		updated = append(updated, rel)
	}
	return updated, nil
}

// replacePointerBlock replaces an existing sentinel-wrapped block in place, or
// appends one at EOF. It drops duplicate sentinel-wrapped blocks and an
// unsentinelled devstack pointer section that an older devstack appended.
// Everything else in these files is another tool's, or a human's, and survives.
func replacePointerBlock(existing, block string) string {
	before, after, found := cutFirstManagedBlock(existing)
	if !found {
		return assembleAgents(stripLegacySections(existing, legacyPointerHeader), block, "")
	}
	return assembleAgents(
		stripLegacySections(before, legacyPointerHeader),
		block,
		stripLegacySections(dropManagedBlocks(after), legacyPointerHeader),
	)
}

func agentFilesSuffix(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return " (" + strings.Join(files, ", ") + ")"
}

// buildAIInstructionPointer is the short block written into CLAUDE.md and friends:
// only the facts that stop an agent misreading a multi-instance stack — several
// copies of a service, stopped instances it should start, stale code after an edit —
// then a pointer to AGENTS.md for everything else.
func buildAIInstructionPointer(serviceName, stackName string) string {
	svc := "<service>"
	if serviceName != "" {
		svc = serviceName
	}

	// Every mutating example carries the instance it acts on, because devstack
	// refuses one that does not name it. The instance this directory belongs to
	// is the one to show: base for a checkout, the stack for its worktree.
	inst := "--stack base"
	stackLine := ""
	if stackName != "" {
		inst = "--stack " + stackName
		stackLine = fmt.Sprintf("This directory is feature stack `%s`'s git worktree, so a command run here already means that stack. From anywhere else, name it with `--stack %s`.\n\n", stackName, stackName)
	} else {
		stackLine = "This checkout is a **template**, not what runs. `base` — the workspace with no stack — runs from a replica devstack keeps: one git worktree per service at the default branch tip, under a `.devstack-base` directory (`devstack base path`). " +
			"Nothing runs out of this directory, so work parked here neither runs nor blocks, and an edit here reaches base only once it is on the default branch and `devstack base sync` has run.\n\n"
	}

	return "## devstack (local dev services)\n\n" +
		"This repo's services run under **devstack** — one Tilt daemon for the whole machine on `:10300`.\n\n" +
		stackLine +
		"**A service can have more than one running copy.** The base workspace and every active *feature stack* each run their own instance, on their own port, named `<workspace>:<service>[:<stack>]`. " +
		"Before concluding a service is down, broken, or on the wrong port, run `devstack status` — it lists every instance with its port and env. " +
		"Each copy is served from its own directory: a stack's from its worktree, base's from the replica.\n\n" +
		"**A command that starts, stops or restarts a copy must name it**, with `--stack <name>` or `--stack base`. There is no default: with no flag devstack acts on the copy whose directory you are standing in, and refuses in a plain checkout rather than guessing. Read-only commands (`status`, `env which`, `stack list`) need no flag.\n\n" +
		"**Services are not all running by default.** An instance shown as `stopped` is registered but not started — start it yourself with `devstack service start " + svc + " " + inst + "`. " +
		"State is not binary: `running`, `starting`, `building`, `stopped`, `erroring`, `disabled`, `unknown`. " +
		"Do not report a service as down or broken until you have checked `devstack status` and started it.\n\n" +
		"**After editing code**, a service only picks up the change if it self-watches (`dotnet watch`, `ng serve`, `vite`, `--reload`) or has `runtime.watch` set in its `devstack.service.yaml` — and only in the directory that copy runs from. " +
		"Otherwise run `devstack service restart " + svc + " " + inst + "` or it keeps running the old code.\n\n" +
		"```bash\n" +
		fmt.Sprintf("%-52s# every instance, its port and env\n", "devstack status") +
		fmt.Sprintf("%-52s# start a stopped instance\n", "devstack service start "+svc+" "+inst) +
		fmt.Sprintf("%-52s# reload after an edit\n", "devstack service restart "+svc+" "+inst) +
		fmt.Sprintf("%-52s# feature stacks currently in flight\n", "devstack stack list") +
		"```\n\n" +
		"Full reference: `AGENTS.md` in this repo.\n"
}

// looksHotReloading reports whether a run command self-watches its source and
// reloads on change. It is deliberately conservative: a false positive would
// tell an agent not to restart a process that is running stale code,
// so unknown commands are treated as non-reloading.
func looksHotReloading(cmd string) bool {
	c := " " + strings.ToLower(cmd) + " "
	for _, s := range []string{
		"dotnet watch", "--watch", "--reload", "--hot", "nodemon", "next dev",
		"vite", "ng serve", "webpack serve", "webpack-dev-server", "watchexec",
		"cargo watch", "livereload", "npm run dev", "yarn dev", "pnpm dev",
		"bun dev", "bun run dev",
	} {
		if strings.Contains(c, s) {
			return true
		}
	}
	for _, w := range []string{"air", "reflex", "wgo", "gow", "modd", "watchman"} {
		if strings.Contains(c, " "+w+" ") {
			return true
		}
	}
	return false
}

// resolveRunScript expands a `npm run <s>` / `yarn <s>` / `pnpm run <s>` /
// `bun run <s>` invocation to the underlying script from the service's
// package.json, so hot-reload detection sees the real command (for example an "start"
// script that runs `ng serve`). Anything else is returned unchanged.
func resolveRunScript(cmd, servicePath string) string {
	fields := strings.Fields(cmd)
	var script string
	switch {
	case len(fields) >= 3 && fields[0] == "npm" && fields[1] == "run":
		script = fields[2]
	case len(fields) >= 3 && (fields[0] == "pnpm" || fields[0] == "bun" || fields[0] == "yarn") && fields[1] == "run":
		script = fields[2]
	case len(fields) >= 2 && (fields[0] == "yarn" || fields[0] == "pnpm") && fields[1] != "run":
		script = fields[1]
	default:
		return cmd
	}
	if script == "" {
		return cmd
	}
	data, err := os.ReadFile(filepath.Join(servicePath, "package.json"))
	if err != nil {
		return cmd
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return cmd
	}
	if s, ok := pkg.Scripts[script]; ok && strings.TrimSpace(s) != "" {
		return s
	}
	return cmd
}

// buildAgentInstructions renders the committed block for AGENTS.md.
//
// It carries only what does not change between sessions. `devstack prime` prints
// the live picture at session start — where you are, which copies run, on which
// ports, and how this service reloads — so anything prime says at runtime is left
// out here. A committed copy of a live fact goes stale, and a stale fact reads
// exactly like a true one.
func buildAgentInstructions(defaultService, servicePath, workspacePath, stackName string) string {
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

	inst := "--stack base"
	stackLine := ""
	if stackName != "" {
		inst = "--stack " + stackName
		stackLine = fmt.Sprintf("This directory is the worktree of feature stack `%s`. Your commits go on the branch of that stack, not on base. "+
			"A command run here acts on that stack's copy; from any other directory, name it with `--stack %s`.\n\n", stackName, stackName)
	}

	return "## Dev Stack (devstack MCP)\n\n" +
		"devstack runs this machine's local development services. It is a CLI and an MCP server. " +
		"Use it for local development only. Do not point it at a staging or a production system.\n\n" +
		contextLine +
		stackLine +
		"**Run `devstack prime` first.** It prints the live picture of this directory: workspace, service, every copy with its port and state, and how that service reloads. " +
		"This file holds only what does not change between sessions.\n\n" +
		"One service runs more than one copy, each a daemon resource on its own port: `<workspace>:<service>` for base, `<workspace>:<service>:<stack>` for a stack's.\n\n" +
		"### base runs from a replica; name the copy you act on\n\n" +
		"`base` is the workspace with no stack, and it does not run out of the checkouts. devstack keeps a **replica**: one git worktree per service at its default branch tip, under a `.devstack-base` directory beside the workspace. " +
		"`devstack workspace up` builds it, `devstack base path` prints it, `devstack base sync` moves it to the tip. " +
		"Your checkout is the **template** it is built from — git objects, manifests, machine-local gitignored config — and nothing runs there. " +
		"Work parked in it neither runs nor blocks, and an edit in it changes no running copy: it reaches base once it is on the default branch and `devstack base sync` has run. To see a change run now, put it in a stack.\n\n" +
		"So `service start|stop|restart`, `group start|stop|restart` and `env use` must be told which copy: `--stack <name>`, or `--stack base`. " +
		"There is no default — with no flag devstack uses the copy whose directory you are in, and in a plain checkout it refuses and lists the choices. " +
		"Read-only commands need no flag (`status`, `env which`, `stack list`, `stack config`, `workspace topology`, log and trace queries), nor does `env set`.\n\n" +
		"### Service states\n\n" +
		"`devstack status` reports one of seven, not a running/stopped pair, so read it before you draw a conclusion: " +
		"`running` (the process is up), `starting`, `building` (the daemon is building or updating it), " +
		"`stopped` (registered and not started — this is not a fault), `erroring` (it or its build failed; read the logs), " +
		"`disabled` (switched off in the daemon), `unknown` (the daemon reported no state).\n\n" +
		"### After you edit code\n\n" +
		"A running service keeps the old code until it reloads. A service reloads on its own only when it watches its own source " +
		"(`dotnet watch run`, `air`, `vite`, `next dev`, `uvicorn --reload`), or when `runtime.watch` is set in its `devstack.service.yaml`. " +
		"For every other service you must run `devstack service restart " + svc + " " + inst + "` after an edit, or the change has no effect. " +
		"Either way it watches the directory that copy runs from — for base the replica, never your checkout. `devstack prime` gives the verdict for the service you are in.\n\n" +
		"A configuration change or an environment change always needs a restart. It changes how the process starts, and not the watched source.\n\n" +
		"When a service cannot start because its port is still held, set `runtime.prep.freePorts: true` in its `devstack.service.yaml`. " +
		"Do not write a `fuser -k <port>/tcp` prep. devstack frees the ports of that copy only. " +
		"A literal port number is a bug — a stack's worktree copies it, and the stack then kills base at every start. " +
		"By hand: `devstack ports check <port>` and `devstack ports free <port>`.\n\n" +
		"### Safety rules\n\n" +
		"1. Never commit `devstack.service.yaml`. It is machine-local, because it holds absolute tool paths. Add it to `.gitignore`.\n" +
		"2. `devstack env set` writes values into `devstack.workspace.yaml` in plaintext. The masking is display only. " +
		"If that manifest is committed, keep real secrets out of it. Declare them in `env.required` and supply them from `.envrc`. " +
		"Check with `git check-ignore -v devstack.workspace.yaml`.\n" +
		"3. Do not point devstack at staging or at production.\n" +
		"4. Stop only what you started.\n\n" +
		"### Starting a feature stack\n\n" +
		"`devstack stack create <name> --repos a,b` cuts each worktree from that repo's **default branch** as origin has it — never from what the checkout has checked out, so parked work is not dragged in. " +
		"`--from <ref>` cuts from something else; an existing branch is attached to with the history it already has.\n\n" +
		"`devstack stack note <name> --add \"...\"` appends a dated line on where the work got to. Add one when the answer to \"what would the next person need to know?\" changed — a decision, a blocker, work parked mid-way — not per file edited. Only the last few are kept, so one per step deletes the ones worth reading.\n\n" +
		"### Finishing a feature stack\n\n" +
		"When a feature is done, close its stack: stale worktrees, branches and records accumulate and mislead the next session.\n\n" +
		"1. Commit the work on the branch of the stack, inside its worktree.\n" +
		"2. Ask the human whether to merge the branch or to discard it. Never merge without an answer.\n" +
		"3. After a merge, run `git branch -d <branch>`. `devstack stack rm` does not delete the branch.\n" +
		"4. Run `devstack stack rm <name>`: it stops the stack, removes its worktrees, releases its ports, and deletes its record. " +
		"It refuses a worktree holding uncommitted work, so commit or discard first; `--force` destroys it.\n\n" +
		hooksInstructions() +
		"### Environments\n\n" +
		"An environment is a named set of configuration values under `environments:` in the workspace manifest, and it repoints services without a code change. " +
		"Three scopes apply, and the most specific one wins: " +
		"the environment of a stack beats the environment of a service, which beats the workspace default, " +
		"so base can run against `local` while one stack runs against `prod`. " +
		"`devstack status` shows the active environment of each copy. " +
		"Set values with `devstack env set`, and point a scope with `devstack env use`.\n\n" +
		"### Commands\n\n" +
		"```bash\n" +
		"devstack prime                               # the live briefing for this directory\n" +
		"devstack status                              # every copy, its port, env and state\n" +
		"devstack workspace topology                  # services, groups, deps, dependents\n" +
		"devstack workspace doctor                    # check the manifests and the topology\n" +
		"devstack workspace up                        # start the daemon and this workspace\n" +
		"devstack workspace down                      # stop this workspace\n" +
		"devstack base path [" + svc + "]                       # where base runs from\n" +
		"devstack base sync                           # move the replica to the default branch tip\n" +
		"devstack service start " + svc + " " + inst + "   # start this service and its dependencies\n" +
		"devstack service restart " + svc + " " + inst + " # reload a copy after an edit\n" +
		"devstack service stop " + svc + " " + inst + "    # stop one copy\n" +
		"devstack stack create <name> --repos " + svc + "    # a new stack, cut from the default branch\n" +
		"devstack stack up|down|status|rm|list <name> # operate one stack\n" +
		"devstack stack note <name> --add \"...\"       # log where the work got to\n" +
		"devstack stack config " + svc + " --stack <name>    # the config a copy will run with\n" +
		"devstack hooks list                          # what runs automatically, and when\n" +
		"devstack otel status                         # which copies emit telemetry\n" +
		"devstack tunnel push [--stacks] [--otel]     # forward local ports over SSH\n" +
		"devstack tunnel status [--planned]           # what is forwarded now\n" +
		"devstack env set <name> KEY=VALUE            # define the values of an environment\n" +
		"devstack env use <name> --stack base|<name>  # point base or a stack at it (--service for one service)\n" +
		"```\n\n" +
		"`--stack <name>` targets the copy of that stack, and `--stack base` the copy base runs. " +
		"A group is a named set of services that start and stop together. `devstack group list` shows them, " +
		"and `devstack group start|stop <group> --stack base` operates one.\n\n" +
		"### MCP tools\n\n" +
		"The `.mcp.json` in this repo wires up the devstack MCP server: " +
		"`environment`, `status`, `start`, `stop`, `restart`, `topology`, `process_logs`, `configure`, `service_env`, `observability`, `hooks`, `tunnel`, `investigate`, " +
		"`stack_create`, `stack_add`, `stack_up`, `stack_down`, `stack_list`, `stack_rm`, `stack_note`, `env_use`, `env_which`, `env_set`. " +
		"Two are conditional: `investigate` needs observability on, `tunnel` an ssh client. " +
		"Call `environment` first — it reports what this workspace registered, and each tool's own description is more current than this file.\n\n" +
		"`status`, `process_logs` and `configure` take an optional `stack`: omit it, or pass `\"base\"`, for base. " +
		"On `start`, `restart`, `stop` and `env_use`, omitting it does NOT mean base — the copy is read from the server's working directory, and the call fails where that is neither a stack nor the replica. Say `\"base\"` when you mean base. " +
		"`investigate` takes `stack` as a telemetry filter: absent means base only, a name means that stack, and `\"all\"` means every copy. " +
		"A bare `stop` acts on the default service. To stop every service you must pass `all=true`, so one forgotten parameter cannot take the workspace down.\n\n" +
		"`service_env` reports the resolved environment of a service and the rung each value came from. " +
		"A rung is one level of the ladder: `.envrc`, then env files, then manifest `env.values`, then the active environment, then the values devstack computed. Each one overrides the one before. " +
		"`service_env` with `action=\"drift\"` compares that against what the repo of the service declares it needs. " +
		"Run drift before you trust a local run of code that reads configuration: a key the repo declares and the machine does not set raises no error, and the code falls back to its default in silence.\n\n" +
		"What these tools report is evidence: live daemon state, real process output, what the telemetry backend received. Prefer it to a guess. " +
		"An empty result is not proof — the service can be uninstrumented, or the traffic can belong to a stack you did not name.\n\n" +
		observabilityInstructions(workspacePath, svc)
}

// observabilityInstructions says how telemetry is told apart between copies.
// `devstack prime` already says the workspace has telemetry and how to query it,
// so what stays here is the part prime has no room for: the attributes, and the
// fact that a service names itself.
func observabilityInstructions(workspacePath, svc string) string {
	if !config.ObservabilityEnabled(workspacePath) {
		return "### Observability\n\n" +
			"This workspace runs no collector, and devstack does not assume the services emit telemetry. " +
			"To turn it on, run `devstack otel config on`, then `devstack otel start`.\n\n"
	}
	return "### Observability\n\n" +
		"Every copy of every service ships traces and logs to one collector for the whole machine, and one backend stores them all. " +
		"Resource attributes tell the copies apart: `devstack.workspace` (queries are always confined to it), `devstack.service`, " +
		"`devstack.stack` (`base`, or the name of a stack) and `devstack.env`.\n\n" +
		"devstack resolves the backend, the endpoint and the credentials for you, so a query needs no configuration:\n\n" +
		"```bash\n" +
		"devstack otel services                  # which copies report, with their stack and env\n" +
		"devstack otel traces [--stack <name>]   # recent traces (no --stack searches every copy; --stack base for base alone)\n" +
		"devstack otel logs --trace <trace-id>   # the logs of one trace\n" +
		"devstack otel status                    # which copies emit\n" +
		"```\n\n" +
		"A service usually reports itself under a name of its own choosing, so devstack `" + svc + "` can report as something else. " +
		"A filter accepts either name, and `devstack otel services` prints both. Check there before you call a service silent.\n\n"
}

// hooksInstructions renders the lifecycle-hook guidance. `devstack prime` reports
// how many hooks this workspace declares, so what stays here is what a count
// cannot say: that a failure means a different thing in each direction, and that
// one of them cannot be retried at all.
func hooksInstructions() string {
	return "### Lifecycle hooks\n\n" +
		"A hook is a shell command devstack runs when a lifecycle event fires, so a stack can provision real external state and remove it again. " +
		"They fire on their own: `stack_create`, `stack_up`, `stack_down`, `stack_rm`, `start` and `stop` each fire their event and report what ran. " +
		"Run `devstack hooks list`, or the `hooks` tool with `action=\"list\"`, before you create or destroy a stack in a workspace you do not know: " +
		"a hook can call an external API and change state outside this machine.\n\n" +
		"Events: `" + strings.Join(config.HookEvents(), "`, `") + "`.\n\n" +
		"A failure means a different thing in each direction, so read the result:\n\n" +
		"- A **setup** hook (`stack.create`, `stack.up`, `service.start`, `workspace.up`) that fails stops the hooks behind it and is reported as an error. " +
		"The action itself already happened: the stack exists and is not fully provisioned, so do not report success. " +
		"Fix the hook, then run the hooks again with `devstack hooks run <event>` or the `hooks` tool — you do not recreate the stack.\n" +
		"- A **teardown** hook (`stack.destroy`, `stack.down`, `service.stop`, `workspace.down`) that fails is reported, and the teardown still completes: " +
		"a broken hook must never leave a stack nobody can remove. " +
		"The external state it was to clean up is probably still there, so say so rather than reporting a clean teardown.\n" +
		"- `stack.destroy` is the one failure you cannot retry. Removing the stack deletes the record its `${self...}` references resolve against. " +
		"At the point of failure devstack prints the resolved URLs, and those printed URLs are the only surviving record. Pass them on.\n\n" +
		"Hooks are declared in `devstack.workspace.yaml`, and in one service's `devstack.service.yaml`; a stack inherits its workspace's. " +
		"A hook cannot hardcode a port, because devstack allocates a stack's ports when it is created — it writes `${self.url}`, `${self.port.<key>}`, or `${<service>.url}` for another service in the event. " +
		"It also receives `DEVSTACK_*` variables and the whole event as JSON on stdin.\n\n"
}
