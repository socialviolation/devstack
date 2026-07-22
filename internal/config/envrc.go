package config

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnvrcFileName is the direnv-style env file devstack resolves per repo.
const EnvrcFileName = ".envrc"

// envrcNoise are shell bookkeeping vars the child always rewrites; they say
// nothing about what .envrc contributed.
var envrcNoise = map[string]bool{
	"_":      true,
	"PWD":    true,
	"OLDPWD": true,
	"SHLVL":  true,
}

// ResolveEnvrc evaluates dir's .envrc in a shell and returns only the variables
// it contributes. A missing .envrc yields an empty map and a nil error.
//
// Errors never carry env values: .envrc files hold live credentials.
func ResolveEnvrc(dir string) (map[string]string, error) {
	return ResolveEnvFile(dir, EnvrcFileName)
}

// ResolveEnvFile evaluates an env file in a shell and returns only the variables
// it contributes. A relative name resolves against dir, which is also the
// shell's working directory. A missing file yields an empty map and a nil error.
//
// Errors never carry env values: these files hold live credentials.
func ResolveEnvFile(dir, name string) (map[string]string, error) {
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, name)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	baseline := os.Environ()

	// `|| exit $?` is load-bearing: bash's `.` returns non-zero on a syntax error
	// but does not abort the shell, so without it a broken .envrc yields partial
	// env and exit 0 — the failure-swallowing bug this replaces.
	ref := shQuote(sourceRef(name))
	// .envrc files commonly use bashisms ([[ ]], arrays); evaluate with bash when
	// present so they resolve as they do in the developer's shell, falling back to
	// sh where bash is absent.
	shell := "sh"
	if p, err := exec.LookPath("bash"); err == nil {
		shell = p
	}
	cmd := exec.Command(shell, "-c", "set -a; . "+ref+" || exit $?; set +a; env -0")
	cmd.Dir = dir
	cmd.Env = baseline

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// An .envrc running under `set -x` traces its own assignments to stderr,
		// values included, so xtrace lines are dropped before the error is built.
		msg := stripXtrace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("evaluate %s: %w", path, err)
		}
		return nil, fmt.Errorf("evaluate %s: %w: %s", path, err, msg)
	}

	// env -0 prints the whole environment, so diff against the baseline the
	// child started from — otherwise every inherited var looks like .envrc set it.
	base := parseEnvEntries(baseline)
	out := map[string]string{}
	for k, v := range parseEnvEntries(splitNUL(stdout.Bytes())) {
		if envrcNoise[k] {
			continue
		}
		if old, ok := base[k]; ok && old == v {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// stripXtrace drops lines prefixed by an unmodified $PS4 ("+ ", nested "++ ").
// PS4 is deliberately left alone: an .envrc can reset it, so it is not trusted.
func stripXtrace(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		plus := 0
		for plus < len(line) && line[plus] == '+' {
			plus++
		}
		if plus > 0 && strings.HasPrefix(line[plus:], " ") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// shQuote single-quotes a string for embedding in a shell command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sourceRef ensures a relative env-file path has a slash so the POSIX `.` builtin
// loads it from the run dir instead of searching $PATH (dash won't fall back to
// the cwd like bash does).
func sourceRef(f string) string {
	if strings.HasPrefix(f, "/") || strings.HasPrefix(f, "./") || strings.HasPrefix(f, "../") {
		return f
	}
	return "./" + f
}

func splitNUL(b []byte) []string {
	var out []string
	for _, e := range bytes.Split(b, []byte{0}) {
		if len(e) > 0 {
			out = append(out, string(e))
		}
	}
	return out
}

func parseEnvEntries(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}
