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
		fmt.Fprintf(os.Stderr, "✓ %s%s\n", svcName, agentFilesSuffix(append([]string{".mcp.json"}, files...)))
	}

	if files, err := writeAIInstructionPointers("", ws.Path, ""); err != nil {
		errs = append(errs, fmt.Sprintf("  workspace root: %v", err))
		fmt.Fprintf(os.Stderr, "✗ workspace root: %v\n", err)
	} else if len(files) > 0 {
		fmt.Fprintf(os.Stderr, "✓ workspace root%s\n", agentFilesSuffix(files))
	}

	errs = append(errs, refreshStackAgentsMD(ws.Name, ws.Path)...)

	if len(errs) > 0 {
		return fmt.Errorf("%d service(s) failed:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return nil
}

// refreshStackAgentsMD rewrites AGENTS.md in every feature stack's worktree
// services, which live outside the base workspace's service paths and would
// otherwise go stale. It returns per-service failure lines rather than aborting,
// so one broken stack does not stop the rest.
func refreshStackAgentsMD(workspaceName, workspacePath string) []string {
	recs, err := stack.LoadStore(workspaceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load stacks for workspace %q: %v\n", workspaceName, err)
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
			fmt.Fprintf(os.Stderr, "✓ %s (stack %s)%s\n", name, rec.Name, agentFilesSuffix(append([]string{".mcp.json"}, files...)))
		}
	}
	return errs
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
)

// writeAgentsMD writes the managed devstack block into AGENTS.md non-destructively.
// If a sentinel-wrapped block exists it is replaced in place; otherwise any legacy
// section is migrated away and a fresh block is appended, preserving all other content.
func writeAgentsMD(serviceName, servicePath, workspacePath, stackName string) error {
	agentsFile := filepath.Join(servicePath, "AGENTS.md")
	existing, err := os.ReadFile(agentsFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read AGENTS.md: %w", err)
	}
	block := agentsSentinelBegin + "\n" + buildAgentInstructions(serviceName, servicePath, workspacePath, stackName) + agentsSentinelEnd
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

// aiInstructionFiles are the instruction files coding agents actually read.
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
// appends one at EOF. Unlike replaceManagedBlock it performs no legacy AGENTS.md
// migration: content outside the sentinels is another tool's, and is preserved.
func replacePointerBlock(existing, block string) string {
	if begin := strings.Index(existing, agentsSentinelBegin); begin != -1 {
		if rel := strings.Index(existing[begin:], agentsSentinelEnd); rel != -1 {
			end := begin + rel + len(agentsSentinelEnd)
			return assembleAgents(existing[:begin], block, existing[end:])
		}
	}
	return assembleAgents(existing, block, "")
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

	stackLine := ""
	if stackName != "" {
		stackLine = fmt.Sprintf("This directory is feature stack `%s`'s git worktree — target its instance with `--stack %s`, or commands act on base.\n\n", stackName, stackName)
	}

	return "## devstack (local dev services)\n\n" +
		"This repo's services run under **devstack** — one Tilt daemon for the whole machine on `:10300`.\n\n" +
		stackLine +
		"**A service can have more than one running copy.** The base workspace and every active *feature stack* each run their own instance, on their own port, named `<workspace>:<service>[:<stack>]`. " +
		"Before concluding a service is down, broken, or on the wrong port, run `devstack status` — it lists every instance with its port and env. " +
		"A stack's copy is served from its own git worktree, not this checkout.\n\n" +
		"**Services are not all running by default.** An instance shown as `stopped` is registered but not started — start it yourself with `devstack start " + svc + "` (add `--stack <name>` for a stack's instance). " +
		"State is not binary: `running`, `starting`, `building`, `stopped`, `erroring`, `disabled`, `unknown`. " +
		"Do not report a service as down or broken until you have checked `devstack status` and started it.\n\n" +
		"**After editing code**, a service only picks up the change if it self-watches (`dotnet watch`, `ng serve`, `vite`, `--reload`) or has `runtime.watch` set in its `devstack.service.yaml`. " +
		"Otherwise run `devstack restart " + svc + "` or it keeps running the old code.\n\n" +
		"```bash\n" +
		fmt.Sprintf("%-39s# every instance, its port and env\n", "devstack status") +
		fmt.Sprintf("%-39s# start a stopped instance (add --stack <name>)\n", "devstack start "+svc) +
		fmt.Sprintf("%-39s# reload after an edit\n", "devstack restart "+svc) +
		fmt.Sprintf("%-39s# feature stacks currently in flight\n", "devstack stack list") +
		"```\n\n" +
		"Full reference: `AGENTS.md` in this repo.\n"
}

// looksHotReloading reports whether a run command self-watches its source and
// reloads on change. It is deliberately conservative: a false positive would
// tell an agent not to restart a process that is actually running stale code,
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
// package.json, so hot-reload detection sees the real command (e.g. an "start"
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

// hotReloadInstructions renders the "reload or restart after an edit" guidance:
// a general rule plus a verdict for this specific service, so an agent knows
// whether its code edits apply live or need a manual restart.
func hotReloadInstructions(serviceName, servicePath, stackName string) string {
	general := "### After you edit code — reload or restart\n\n" +
		"A running service keeps executing its **old** code until it is reloaded. A service reloads automatically only if it **self-watches** — a hot-reload run command such as `dotnet watch run`, `air`, `vite` / `next dev`, or `uvicorn --reload` — or has **`runtime.watch`** set in its `devstack.service.yaml`, which has devstack watch those paths and restart it on change (debounced). " +
		"If a service has neither, after editing its source you **must** run `devstack restart <service>` (add `--stack <name>` for a stack instance) or your change has no effect. " +
		"Prefer hot-reloading run commands; when a service can't self-reload, add `runtime.watch: [<source dirs>]` so devstack reloads it for you. " +
		"Config/env changes (`devstack env set` / `env use`) always need a restart, even for a self-reloading service — they change the launch environment, not the watched source.\n\n"

	if serviceName == "" {
		return general
	}
	m, err := config.LoadServiceManifest(servicePath)
	if err != nil || m == nil || strings.TrimSpace(m.Runtime.Run.Command) == "" {
		return general
	}
	cmd := m.Runtime.Run.Command
	restartCmd := fmt.Sprintf("devstack restart %s", serviceName)
	if stackName != "" {
		restartCmd += fmt.Sprintf(" --stack %s", stackName)
	}
	switch {
	case looksHotReloading(cmd) || looksHotReloading(resolveRunScript(cmd, servicePath)):
		return general + fmt.Sprintf("**`%s` hot-reloads** via its run command (`%s`) — your source edits apply automatically; do not restart it for code changes.\n\n", serviceName, cmd)
	case len(m.Runtime.Watch) > 0:
		return general + fmt.Sprintf("**`%s` auto-restarts on change** — `runtime.watch` is set, so devstack reloads it after your edits (no manual restart for code changes).\n\n", serviceName)
	default:
		return general + fmt.Sprintf("**`%s` does NOT hot-reload** (run command `%s`) — after editing its source you MUST run `%s`, or it keeps running the old code. To stop restarting by hand, switch it to a watch command (e.g. `dotnet watch run`, `air`, `uvicorn --reload`) or add `runtime.watch: [<source dirs>]` to its `devstack.service.yaml`.\n\n", serviceName, cmd, restartCmd)
	}
}

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

	stackLine := ""
	if stackName != "" {
		stackLine = fmt.Sprintf("**You are in feature stack `%s`'s worktree.** Edits here stay on this stack's branch, not base. Target this instance with `--stack %s` (e.g. `devstack restart %s --stack %s`); without it, commands act on base.\n\n", stackName, stackName, svc, stackName)
	}

	observabilityBlock := "### Observability\n\n" +
		"Not enabled for this workspace — services are not assumed to be OTEL-instrumented and no collector runs. " +
		"Turn it on with `devstack otel enable`, then `devstack otel start`.\n\n"
	if config.ObservabilityEnabled(workspacePath) {
		observabilityBlock = "### Observability\n\n" +
			"Services ship traces and logs to one collector for the whole machine (gRPC `localhost:4317`), which stores them in one backend shared by every workspace and stack.\n\n" +
			"**Every instance of a service reports to that one backend** (telemetry output calls an instance a *variant*, which is the same thing), so telemetry is told apart by resource attributes rather than by where it is stored:\n\n" +
			"| Attribute | What it identifies |\n" +
			"|---|---|\n" +
			"| `devstack.workspace` | the workspace — queries are scoped to it automatically, always |\n" +
			"| `devstack.service` | the service as devstack names it (what you filter on) |\n" +
			"| `devstack.stack` | which instance: `base`, or a feature stack's name |\n" +
			"| `devstack.env` | the config env that instance runs under (e.g. `dev`, `perf`) |\n\n" +
			"**Query it without configuring anything** — devstack resolves the backend, endpoint and credentials for you, and confines every query to this workspace:\n\n" +
			"```bash\n" +
			"devstack otel services                  # which instances are reporting, and their stack/env\n" +
			"devstack otel traces                    # recent traces (defaults to the service you are in)\n" +
			"devstack otel traces --stack <name>     # only that stack's instance\n" +
			"devstack otel traces --service all      # every service in the workspace\n" +
			"devstack otel traces <trace-id>         # full span tree for one trace\n" +
			"devstack otel logs --trace <trace-id>   # logs correlated with that trace\n" +
			"devstack otel status                    # per-instance evidence: which ones are actually emitting\n" +
			"```\n\n" +
			"**A service usually reports itself under a different name than devstack knows it by** (devstack `" + svc + "` may report as something else entirely). Filters accept either name, and `devstack otel services` prints both — check there before concluding a service is silent.\n\n" +
			"**Comparing a stack against base** is the common debugging move: run the same query with `--stack <name>` and with `--stack base`, and diff what comes back. Without `--stack` you get the base instance only, so a stack's traffic will look missing if you forget it.\n\n" +
			"Route telemetry upstream instead with `devstack otel configure`; open the UI with `devstack otel open`. " +
			"Per-developer endpoint override: set `OTEL_EXPORTER_OTLP_ENDPOINT` in `.envrc`.\n\n"
	}

	return "## Dev Stack (devstack MCP)\n\n" +
		"devstack is a CLI and MCP server that gives agents programmatic control over a local development stack. " +
		"It runs services on top of [Tilt](https://tilt.dev), handling lifecycle, dependency ordering, and observability. " +
		"Local dev only — never point devstack at staging or production.\n\n" +
		contextLine +
		stackLine +
		"### One host daemon\n\n" +
		"devstack runs a single Tilt daemon for the whole machine (port 10300). " +
		"Every active workspace's services run inside that one daemon, alongside the services of every active feature stack. " +
		"There is no daemon per workspace and none per stack — one daemon holds them all.\n\n" +
		"### Which instance am I operating on\n\n" +
		"Every running service is a Tilt resource whose name tells you which instance you are touching:\n\n" +
		"- Base service: `<workspace>:<service>` (e.g. `" + wsBaseName(workspacePath) + ":" + svc + "`).\n" +
		"- A feature stack's copy of that service: `<workspace>:<service>:<stack>`.\n\n" +
		"*Base* is the workspace's own checkout and the instance it runs — what every command acts on when you pass no `--stack`, and spelled literally as `base` where the service-control and telemetry tools take a stack name; the stack tools (`stack_up`, `stack_down`, `stack_rm`, `env_use`, `env_which`, `service_env`) have no such value — omit the parameter instead. " +
		"The base instance and each stack's instance listen on **different ports**, and every command names which instance it acted on (a `target:` line naming the stack, blank for base) — so ports plus that target line tell you which running copy you are inspecting or controlling.\n\n" +
		hotReloadInstructions(defaultService, servicePath, stackName) +
		"### Feature stacks\n\n" +
		"A feature stack is a parallel version of one or more services, run from a git worktree on a feature branch, " +
		"on its own dynamically-allocated port, beside the base — reusing the base stack for every service it does not change. " +
		"Two stacks means two worktrees, two branches, and two extra ports, all live at once in the one host daemon. " +
		"`devstack stack list` says what each one overlays, what branch it is on, how old it is, and its note — set the note with `devstack stack note <name>` so a stack you come back to next week still explains itself. " +
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
		"### Service states\n\n" +
		"`devstack status` reports one of: `running` (process up), `starting` (coming up), `building` (daemon is building/updating it), " +
		"`stopped` (registered but not started), `erroring` (the service or its build failed — check logs), `disabled` (switched off in the daemon), " +
		"`unknown` (daemon reported no state). It is not a running/stopped binary — read the actual state before concluding anything.\n\n" +
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
		"devstack stack list                          # what each stack overlays, its branch, age and note\n" +
		"devstack stack note <name> \"...\"             # record what a stack is for (ticket URL, issue key, a sentence)\n" +
		"devstack stack config " + svc + " --stack <name>    # effective config a stack's service runs with\n" +
		"devstack tunnel push [--stacks] [--otel]     # forward local service ports over SSH (--stacks adds stack instances, --otel adds the observability UI)\n" +
		"devstack env set <name> KEY=VALUE            # define an environment's config-patch values\n" +
		"devstack env use <name> [--service|--stack]  # point base, a service, or a stack at env <name>\n" +
		"devstack env which [--service|--stack]        # which env an instance resolves to, and its values\n" +
		"```\n\n" +
		"`--stack <name>` targets that stack's instance instead of base; without it commands operate on the base workspace. " +
		"A *group* is a named set of services started and stopped together (`devstack groups list`); `start`, `restart`, `stop` and `process_logs` take a group name in place of a service.\n\n" +
		"### Environments (where a service points)\n\n" +
		"An **environment** (`environments:` in the workspace manifest) is a named bundle of config-var patches — DB URLs, feature flags, external endpoints — that repoints services without code changes. It applies at three scopes, most-specific winning: a **stack**'s env beats a **service**'s env beats the **workspace** default. So base can run against `local` while one stack runs against `prod`. `devstack status` shows each instance's active env (the ENV column / `env:<name>`), so you can see where every running copy is pointed. Set values with `devstack env set` — they are written into the workspace manifest in plaintext (masking is display-only), so if that manifest is committed keep real secrets out and declare them in `env.required` instead; point a scope with `devstack env use`.\n\n" +
		"### MCP tools\n\n" +
		"The `.mcp.json` in this repo wires up the devstack MCP server — the agent interface. The tools are " +
		"`environment`, `status`, `start`, `stop`, `restart`, `topology`, `process_logs`, `configure`, `service_env`, `observability`, `tunnel` and `investigate`; " +
		"the stack tools `stack_create`, `stack_up`, `stack_down`, `stack_list`, `stack_rm`; and the env tools `env_use`, `env_which`, `env_set`. " +
		"Two are conditional: `investigate` is registered only while observability is enabled, `tunnel` only when an ssh client is available. " +
		"Call `environment` first: it reports what is actually registered here, and each tool's own description is more current than this file.\n\n" +
		"Feature stacks and environments can be driven end to end over MCP. Some things still need the shell: `devstack workspace up` / `workspace down`, `devstack workspace doctor`, `devstack stack config`, " +
		"and the otel commands past status, variants and trace queries (`otel traces`, `otel logs --trace`, `otel open`). What `otel services` prints is available over MCP as observability action=variants.\n\n" +
		"A bare `stop` acts on the default service, like `start` and `restart`; stopping every service takes `all=true`, so a forgotten parameter cannot take the workspace down. `status` reports each service's RELOAD mode — `auto` means source edits apply on their own, `manual` means restart it after editing or it keeps running the old code. `restart` takes `wait_seconds` to wait for the rebuild to settle rather than returning the moment it is triggered.\n\n" +
		"The service-control tools (`status`, `start`, `restart`, `stop`, `process_logs`, `configure`) " +
		"take an optional `stack` parameter to target a stack's instance rather than base (omit it, or pass `\"base\"`, for base). " +
		"`investigate` takes `stack` as a telemetry filter: absent means the base instance only, a name means that stack, `\"all\"` means every instance — so an unqualified call will not show you a feature stack's traffic. It is always confined to this workspace, and an unqualified call narrows to the service being worked in. " +
		"`service_env` reports a service's resolved env with the *rung* each value came from. A rung is one level of the precedence ladder: `.envrc`, then env files, then manifest `env.values`, then the active env, then devstack's computed values, each overriding the one before. " +
		"`service_env` with `action=\"drift\"` compares that resolved env against what the service's own repo declares it needs. Run drift before you trust a local run of a code path that reads config: a key the repo declares but the machine has not set does not error, it silently falls back to the code's default.\n\n" +
		"What these tools report is evidence: the daemon's live state, real process output, and what the backend actually received. Prefer it to guessing. An empty result is not proof — the service may not be instrumented, or the traffic may have gone to a stack you did not name.\n\n" +
		"Rules:\n" +
		"1. Check `topology` before making dependency claims.\n" +
		"2. Prefer what the tools observed (live status, process logs, telemetry) to guessing; when telemetry is partial, fall back to process logs and live status.\n" +
		"3. Do not use devstack against staging or production.\n" +
		"4. Never commit `devstack.service.yaml` — it is machine-local (absolute tool paths); gitignore it.\n" +
		"5. `devstack env set` writes values into `devstack.workspace.yaml` in plaintext (masking is display-only). If that manifest is committed, keep real secrets out of it — declare them in `env.required` and supply them from `.envrc`. Check with `git check-ignore -v devstack.workspace.yaml`.\n\n" +
		observabilityBlock
}

func wsBaseName(workspacePath string) string {
	if workspacePath == "" {
		return "<workspace>"
	}
	return filepath.Base(workspacePath)
}
