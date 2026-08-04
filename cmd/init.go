package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	Short: "Register a service and connect it to the devstack MCP server",
	Long: `Register a service in the workspace, and write the configuration that AI agents read.

devstack writes no instructions into your repository. 'devstack prime' prints the
same facts at each session start, and it can not go stale. Where devstack wrote
instructions before, init removes them.

FIRST SETUP OF A SERVICE (give --name, --path and --cmd)
  devstack registers a new service, so that it can run the service and observe it:
    1. It writes devstack.service.yaml in the repo of the service. That file says how devstack runs it
    2. It adds the repo to the repoDiscovery.repos list in the workspace manifest
    3. It writes .mcp.json, which connects an AI agent to the devstack MCP server
    4. It generates the daemon configuration again, so that 'devstack service start' can run the service

  To overwrite a service manifest that exists, give --force. Use it to change the run command.

REFRESH ONLY (no --name, no --path, no --cmd)
  devstack writes .mcp.json again in the current service directory, and it
  removes the instructions that an older devstack left in AGENTS.md, CLAUDE.md
  and the files beside them. To do this for every service in the workspace, give
  --all. To do it for every workspace on this machine, run 'devstack migrate'.

  devstack removes only what devstack wrote. Where a file holds your own text,
  the file stays and your text stays. Where a file holds devstack content only,
  devstack deletes the file.

SESSION BRIEFING (--claude-hook)
  devstack writes a Claude Code SessionStart hook into .claude/settings.json.
  Each session then starts with the live briefing that 'devstack prime' prints.
  devstack merges the hook into the file. It keeps every other key, and every
  hook that you declared before.

  CAUTION: this flag is off by default. The file .claude/settings.json is
  committed, so the hook runs for every person who clones the repo.

LANGUAGE DETECTION
  devstack looks in --path for a known file:
    *.csproj          → dotnet
    go.mod            → go
    requirements.txt  → python
    package.json      → node
  To set the language yourself, give --language.

EXAMPLES
  devstack init --name=api --path=/dev/myorg/api --cmd="go run ."
  devstack init --name=api --path=/dev/myorg/api --cmd="go run ." --force
  devstack init                      # refresh .mcp.json in the current directory
  devstack init --all                # refresh .mcp.json in every service
  devstack init --all --claude-hook  # also brief each Claude Code session`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().String("name", "", "Service name")
	initCmd.Flags().String("path", "", "Absolute path to the service directory")
	initCmd.Flags().String("cmd", "", "Command to run the service (for example \"go run .\" or \"dotnet run\")")
	initCmd.Flags().Int("port", 0, "HTTP port the service listens on. devstack then adds health checks and dashboard links")
	initCmd.Flags().String("language", "", "Language of the service: dotnet, python, node or go. Default: devstack reads it from the files")
	initCmd.Flags().String("group", "", "Group to suggest for the service. To add the service to that group, run 'devstack group add'")
	initCmd.Flags().Bool("all", false, "Refresh every service registered in the workspace")
	initCmd.Flags().Bool("force", false, "Overwrite the service configuration that exists")
	initCmd.Flags().Bool("claude-hook", false, "Also write the Claude Code SessionStart hook into .claude/settings.json, so every session is briefed by 'devstack prime'")
}

func runInit(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	name, _ := cmd.Flags().GetString("name")
	claudeHook, _ := cmd.Flags().GetBool("claude-hook")

	if all {
		return runInitAll(claudeHook)
	}
	if name == "" {
		return runInitRefresh(cmd, claudeHook)
	}
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

// runInitRefresh refreshes .mcp.json in the current directory, and removes the
// instructions an older devstack wrote there.
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
		// called, and .mcp.json names the service for every tool call.
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
		return fmt.Errorf("can not read the working directory: %w", err)
	}

	if err := refreshMCPJson(cwd, defaultService); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "✓ .mcp.json updated for service %q\n", defaultService)
	}

	reportChanges(os.Stderr, stripDir(cwd))
	applyClaudeHook(cwd, claudeHook)
	claudeHookHint(claudeHook)
	return nil
}

// reportChanges prints one line for each file devstack changed, and for each
// file that needs a human.
func reportChanges(w io.Writer, changes []fileChange) {
	for _, c := range changes {
		mark := "✓"
		if !c.Changed() {
			mark = "!"
		}
		fmt.Fprintf(w, "%s %s\n", mark, strings.TrimSpace(describeChange(c)))
	}
}

// runInitAll refreshes every service registered in the workspace.
func runInitAll(claudeHook bool) error {
	ws, err := resolveWorkspace("")
	if err != nil {
		return fmt.Errorf("can not find a workspace for the current directory: %w\nRun this command inside a registered workspace.", err)
	}

	cfg, err := config.Load(ws.Path)
	if err != nil {
		return fmt.Errorf("can not load the workspace configuration: %w", err)
	}

	if len(cfg.ServicePaths) == 0 {
		fmt.Fprintln(os.Stderr, "No service is registered in this workspace. To add one, run 'devstack init --name=...'.")
		return nil
	}

	services := make([]string, 0, len(cfg.ServicePaths))
	for name := range cfg.ServicePaths {
		services = append(services, name)
	}
	sort.Strings(services)

	fmt.Fprintf(os.Stderr, "devstack refreshes .mcp.json for %d services in workspace '%s'\n", len(services), ws.Name)

	var errs []string
	for _, svcName := range services {
		svcPath := cfg.ServicePaths[svcName]
		if err := refreshMCPJson(svcPath, svcName); err != nil {
			errs = append(errs, fmt.Sprintf("  %s: %v", svcName, err))
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", svcName, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "✓ %s (.mcp.json)\n", svcName)
		reportChanges(os.Stderr, stripDir(svcPath))
		applyClaudeHook(svcPath, claudeHook)
	}

	reportChanges(os.Stderr, stripDir(ws.Path))
	applyClaudeHook(ws.Path, claudeHook)
	errs = append(errs, refreshStackServices(ws.Name, claudeHook)...)
	claudeHookHint(claudeHook)

	if len(errs) > 0 {
		return fmt.Errorf("%d services failed:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return nil
}

// refreshStackServices refreshes the worktree of every feature stack. Those
// directories live outside the base workspace's service paths, so nothing else
// reaches them. It returns per-service failure lines rather than aborting, so
// one broken stack does not stop the rest.
func refreshStackServices(workspaceName string, claudeHook bool) []string {
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
			if err := refreshMCPJson(svc.RepoPath, name); err != nil {
				errs = append(errs, fmt.Sprintf("  %s (stack %s): %v", name, rec.Name, err))
				fmt.Fprintf(os.Stderr, "✗ %s (stack %s): %v\n", name, rec.Name, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "✓ %s (stack %s) (.mcp.json)\n", name, rec.Name)
			reportChanges(os.Stderr, stripDir(svc.RepoPath))
			applyClaudeHook(svc.RepoPath, claudeHook)
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
		return fmt.Errorf("if you give --name, you must also give --path")
	}
	if serveCmd == "" {
		return fmt.Errorf("if you give --name, you must also give --cmd")
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
		fmt.Fprintf(os.Stderr, "Detected language: %s\n", lang)
	}

	// This workspace must be manifest-based — init writes manifests, and the
	// Tiltfile is generated from them.
	if !config.HasWorkspaceManifest(ws.Path) {
		return fmt.Errorf("workspace %q has no %s. Run 'devstack workspace add' first", ws.Name, config.WorkspaceManifestFileName)
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
		return fmt.Errorf("service %q has %s already. To overwrite it, give --force", name, config.ServiceManifestFileName)
	}
	if err := writeServiceManifest(path, name, serveCmd, port, langEnv); err != nil {
		return fmt.Errorf("can not write the service manifest: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Wrote %s\n", manifestPath)

	// 2. Register the repo in the workspace manifest's repos list.
	rel := path
	if r, relErr := filepath.Rel(ws.Path, path); relErr == nil {
		rel = "./" + filepath.ToSlash(r)
	}
	if err := config.AddServiceRepo(ws.Path, rel); err != nil {
		return fmt.Errorf("can not register the service in the workspace manifest: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Registered %s in %s\n", rel, config.WorkspaceManifestFileName)

	// 3. Write .mcp.json for AI agent access.
	mcpFile := filepath.Join(path, ".mcp.json")
	if _, err := os.Stat(mcpFile); os.IsNotExist(err) || force {
		if err := writeMCPJson(mcpFile, name); err != nil {
			return fmt.Errorf("can not write .mcp.json: %w", err)
		}
		fmt.Fprintf(os.Stderr, "✓ Wrote .mcp.json\n")
	} else {
		fmt.Fprintf(os.Stderr, ".mcp.json exists already, so devstack keeps it. To overwrite it, give --force\n")
	}

	// 4. Remove the instructions an older devstack wrote here.
	reportChanges(os.Stderr, stripDir(path))

	applyClaudeHook(path, claudeHook)

	// 5. Regenerate the Tiltfile so the daemon picks up the new service.
	if _, err := regenerateHostTiltfile(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: can not generate the Tiltfile again: %v\n", err)
	}

	// Summary
	fmt.Printf("\n✓ %q registered in workspace %q\n\n", name, ws.Name)
	fmt.Printf("  manifest:   %s\n", manifestPath)
	fmt.Printf("  .mcp.json:  %s\n", mcpFile)
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
		return fmt.Errorf("can not write .mcp.json: %w", err)
	}
	return nil
}

// writeMCPJson creates a .mcp.json file in the service directory.
func writeMCPJson(mcpFile, serviceName string) error {
	data, err := mcpJSONContent(filepath.Dir(mcpFile), serviceName)
	if err != nil {
		return err
	}
	return os.WriteFile(mcpFile, data, 0644)
}

// ensureMCPJson writes .mcp.json only where it is absent or different, and
// reports whether it wrote. A migration that rewrites an identical file reports
// a change that is not one, and the next run reports it again.
func ensureMCPJson(serviceDir, serviceName string) (bool, error) {
	path := filepath.Join(serviceDir, ".mcp.json")
	data, err := mcpJSONContent(serviceDir, serviceName)
	if err != nil {
		return false, err
	}
	if existing, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(existing, data) {
		return false, nil
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return false, fmt.Errorf("devstack can not write .mcp.json: %w", err)
	}
	return true, nil
}

// mcpJSONContent renders the .mcp.json of one service directory.
func mcpJSONContent(serviceDir, serviceName string) ([]byte, error) {
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
		return nil, err
	}
	return append(data, '\n'), nil
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
