package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
)

// Reload modes reported per service, answering "must I restart after editing?".
const (
	coreReloadAuto    = "auto"
	coreReloadManual  = "manual"
	coreReloadUnknown = "-"
)

// coreReloadMode classifies whether a running service picks up source edits by
// itself: it does when its run command watches its own source, or when
// runtime.watch has devstack restart it on change. Unknown commands classify as
// manual — a wrong "auto" would have an agent read stale behaviour as its edit.
func coreReloadMode(m *config.ServiceManifest, repoPath string) string {
	if m == nil {
		return coreReloadUnknown
	}
	cmd := strings.TrimSpace(m.Runtime.Run.Command)
	if cmd == "" && len(m.Runtime.Watch) == 0 {
		return coreReloadUnknown
	}
	if coreWatchCommand(cmd) || coreWatchCommand(coreResolveRunScript(cmd, repoPath)) {
		return coreReloadAuto
	}
	if len(m.Runtime.Watch) > 0 {
		return coreReloadAuto
	}
	return coreReloadManual
}

// coreWatchCommand reports whether a run command self-watches its source and
// reloads on change.
func coreWatchCommand(cmd string) bool {
	c := strings.ToLower(cmd)
	// Multi-word phrases are distinctive enough to match anywhere.
	for _, phrase := range []string{
		"dotnet watch", "next dev", "ng serve", "webpack serve", "webpack-dev-server",
		"cargo watch", "npm run dev", "yarn dev", "pnpm dev", "bun dev", "bun run dev",
	} {
		if strings.Contains(c, phrase) {
			return true
		}
	}
	// Single tokens match only as whole words: "vite" would otherwise fire on
	// vitess, and a wrong "auto" tells an agent its edit is live when the old
	// code is still running.
	tokens := map[string]bool{
		"vite": true, "nodemon": true, "watchexec": true, "livereload": true,
		"air": true, "reflex": true, "wgo": true, "gow": true, "modd": true,
		"watchman": true, "--watch": true, "--reload": true, "--hot": true,
		"-w": true,
	}
	for _, field := range strings.FieldsFunc(c, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '=' || r == '/' || r == '\\'
	}) {
		if tokens[field] {
			return true
		}
	}
	return false
}

// coreResolveRunScript expands a package-manager script invocation to the
// command it actually runs, so classification sees the real command (e.g. a
// "start" script that runs `ng serve`). Anything else is returned unchanged.
func coreResolveRunScript(cmd, repoPath string) string {
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
	if script == "" || repoPath == "" {
		return cmd
	}
	data, err := os.ReadFile(filepath.Join(repoPath, "package.json"))
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
