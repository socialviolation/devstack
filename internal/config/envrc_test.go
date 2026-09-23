package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeEnvrc(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, EnvrcFileName), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveEnvrc(t *testing.T) {
	tests := []struct {
		name    string
		envrc   string
		parent  map[string]string
		want    map[string]string
		absent  []string
		wantErr bool
	}{
		{
			name:  "plain exports",
			envrc: "export API_URL=http://localhost:8080\nexport LOG_LEVEL=debug\n",
			want:  map[string]string{"API_URL": "http://localhost:8080", "LOG_LEVEL": "debug"},
		},
		{
			name:   "inherited vars are excluded",
			envrc:  "export ONLY_MINE=yes\n",
			parent: map[string]string{"DEVSTACK_INHERITED": "from-parent"},
			want:   map[string]string{"ONLY_MINE": "yes"},
			absent: []string{"DEVSTACK_INHERITED", "HOME", "PATH"},
		},
		{
			name:   "inherited var reassigned to a new value is captured",
			envrc:  "export DEVSTACK_INHERITED=overridden\n",
			parent: map[string]string{"DEVSTACK_INHERITED": "from-parent"},
			want:   map[string]string{"DEVSTACK_INHERITED": "overridden"},
		},
		{
			name:   "conditional ignores the caller env",
			envrc:  `if [ "$DEVSTACK_STAGE" = "dev" ]; then export DB=dev_url; else export DB=prod_url; fi` + "\n",
			parent: map[string]string{"DEVSTACK_STAGE": "dev"},
			want:   map[string]string{"DB": "prod_url"},
		},
		{
			name:  "interpolation with fallback",
			envrc: `export E="${DEVSTACK_ABSENT:-fallback}"` + "\n",
			want:  map[string]string{"E": "fallback"},
		},
		{
			name:   "interpolation falls back when only the caller sets the var",
			envrc:  `export E="${DEVSTACK_HOST:-fallback}/api"` + "\n",
			parent: map[string]string{"DEVSTACK_HOST": "http://real"},
			want:   map[string]string{"E": "fallback/api"},
		},
		{
			name:  "non-export assignment then conditional on it",
			envrc: "STAGE=dev  # local default\nif [ \"$STAGE\" = \"dev\" ]; then export DB=dev_url; else export DB=prod_url; fi\n",
			want:  map[string]string{"STAGE": "dev", "DB": "dev_url"},
		},
		{
			name:  "value containing equals and newline",
			envrc: "export TOKEN='a=b=c'\nexport PEM='line1\nline2'\n",
			want:  map[string]string{"TOKEN": "a=b=c", "PEM": "line1\nline2"},
		},
		{
			name:    "explicit failure",
			envrc:   "export SECRET=hunter2\nexit 1\n",
			wantErr: true,
		},
		{
			name:    "syntax error",
			envrc:   "export SECRET=hunter2\nif [ ; then\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.parent {
				t.Setenv(k, v)
			}
			dir := writeEnvrc(t, tt.envrc)

			got, err := ResolveEnvrc(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (result %v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveEnvrc: %v", err)
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("%s = %q, want %q", k, got[k], want)
				}
			}
			for _, k := range tt.absent {
				if _, ok := got[k]; ok {
					t.Errorf("%s must not be reported as set by .envrc", k)
				}
			}
		})
	}
}

// A credential helper needs the caller's session variables, and their per-caller
// values must never reach serve_env.
func TestResolveEnvrcSessionVars(t *testing.T) {
	t.Run("a helper-style file reads a session variable", func(t *testing.T) {
		runtimeDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(runtimeDir, "token"), []byte("sk-from-helper"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
		dir := writeEnvrc(t, "export TOKEN=$(cat \"$XDG_RUNTIME_DIR/token\")\n")

		got, err := ResolveEnvrc(dir)
		if err != nil {
			t.Fatalf("ResolveEnvrc: %v", err)
		}
		if got["TOKEN"] != "sk-from-helper" {
			t.Errorf("TOKEN = %q, want the value the helper read", got["TOKEN"])
		}
	})

	t.Run("a re-exported session variable is not a contributed value", func(t *testing.T) {
		t.Setenv("LANG", "en_AU.UTF-8")
		t.Setenv("SSH_AUTH_SOCK", "/run/user/1000/keyring/ssh")
		dir := writeEnvrc(t, "export LANG=C\nexport SSH_AUTH_SOCK=/tmp/other\nexport REAL=yes\n")

		got, err := ResolveEnvrc(dir)
		if err != nil {
			t.Fatalf("ResolveEnvrc: %v", err)
		}
		if got["REAL"] != "yes" {
			t.Errorf("REAL = %q, want yes", got["REAL"])
		}
		for _, k := range []string{"LANG", "SSH_AUTH_SOCK"} {
			if v, ok := got[k]; ok {
				t.Errorf("%s = %q, want it absent from the result", k, v)
			}
		}
	})
}

func TestResolveEnvrcMissingFile(t *testing.T) {
	got, err := ResolveEnvrc(t.TempDir())
	if err != nil {
		t.Fatalf("missing .envrc must not error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map, got %v", got)
	}
}

func TestResolveEnvrcReturnsOnlyEnvrcKeys(t *testing.T) {
	t.Setenv("DEVSTACK_INHERITED", "from-parent")
	dir := writeEnvrc(t, "export ONLY_MINE=yes\n")

	got, err := ResolveEnvrc(dir)
	if err != nil {
		t.Fatalf("ResolveEnvrc: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 key (ONLY_MINE), got %d: %v", len(got), keysOf(got))
	}
}

func TestResolveEnvrcErrorOmitsValues(t *testing.T) {
	t.Setenv("DEVSTACK_INHERITED", "parent-secret")
	dir := writeEnvrc(t, "export API_KEY=sk-live-abc123\nexport DB_PASSWORD=hunter2\nexit 1\n")

	_, err := ResolveEnvrc(dir)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	for _, secret := range []string{"sk-live-abc123", "hunter2", "parent-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks value %q: %s", secret, err.Error())
		}
	}
}

func TestResolveEnvrcErrorOmitsXtracedValues(t *testing.T) {
	t.Run("xtraced secret is filtered out", func(t *testing.T) {
		dir := writeEnvrc(t, "set -x\nexport API_KEY=sk-live-SECRET456\nexport PEM=\"multi\nline-SECRET789\"\nexit 1\n")

		_, err := ResolveEnvrc(dir)
		if err == nil {
			t.Fatal("want error, got nil")
		}
		for _, secret := range []string{"sk-live-SECRET456", "line-SECRET789"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaks xtraced value %q: %s", secret, err.Error())
			}
		}
	})

	t.Run("genuine diagnostics still reach the caller", func(t *testing.T) {
		dir := writeEnvrc(t, "export A=1\nif [ ; then\n")

		_, err := ResolveEnvrc(dir)
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "syntax error") {
			t.Fatalf("error must carry sh's diagnostic, got %q", err.Error())
		}
	})
}

func TestResolveEnvrcIsIndependentOfTheCallerEnv(t *testing.T) {
	const body = "export OPENROUTER_API_KEY=from-file\n"

	tests := []struct {
		name   string
		parent map[string]string
	}{
		{name: "caller does not set the key"},
		{name: "caller sets the same value", parent: map[string]string{"OPENROUTER_API_KEY": "from-file"}},
		{name: "caller sets a different value", parent: map[string]string{"OPENROUTER_API_KEY": "from-shell"}},
	}

	var first map[string]string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.parent {
				t.Setenv(k, v)
			}

			got, err := ResolveEnvrc(writeEnvrc(t, body))
			if err != nil {
				t.Fatalf("ResolveEnvrc: %v", err)
			}
			if got["OPENROUTER_API_KEY"] != "from-file" {
				t.Errorf("OPENROUTER_API_KEY = %q, want %q", got["OPENROUTER_API_KEY"], "from-file")
			}
			if first == nil {
				first = got
				return
			}
			if !reflect.DeepEqual(got, first) {
				t.Errorf("resolved env differs between callers: %v, want %v", got, first)
			}
		})
	}
}

func TestResolveEnvrcOmitsCallerVarsTheFileDoesNotMention(t *testing.T) {
	t.Setenv("DEVSTACK_CALLER_ONLY", "from-shell")
	dir := writeEnvrc(t, "export FROM_FILE=yes\n")

	got, err := ResolveEnvrc(dir)
	if err != nil {
		t.Fatalf("ResolveEnvrc: %v", err)
	}
	if !reflect.DeepEqual(got, map[string]string{"FROM_FILE": "yes"}) {
		t.Fatalf("got %v, want only FROM_FILE", got)
	}
}

func TestResolveEnvrcKeepsHomeInTheBaseline(t *testing.T) {
	dir := writeEnvrc(t, `export CACHE_DIR="$HOME/.cache/devstack"`+"\n")

	got, err := ResolveEnvrc(dir)
	if err != nil {
		t.Fatalf("ResolveEnvrc: %v", err)
	}
	want := os.Getenv("HOME") + "/.cache/devstack"
	if got["CACHE_DIR"] != want {
		t.Fatalf("CACHE_DIR = %q, want %q", got["CACHE_DIR"], want)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
