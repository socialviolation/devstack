package config

import (
	"os"
	"path/filepath"
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
			name:   "conditional takes the dev branch",
			envrc:  `if [ "$DEVSTACK_STAGE" = "dev" ]; then export DB=dev_url; else export DB=prod_url; fi` + "\n",
			parent: map[string]string{"DEVSTACK_STAGE": "dev"},
			want:   map[string]string{"DB": "dev_url"},
		},
		{
			name:   "conditional takes the prod branch",
			envrc:  `if [ "$DEVSTACK_STAGE" = "dev" ]; then export DB=dev_url; else export DB=prod_url; fi` + "\n",
			parent: map[string]string{"DEVSTACK_STAGE": "live"},
			want:   map[string]string{"DB": "prod_url"},
		},
		{
			name:  "interpolation with fallback",
			envrc: `export E="${DEVSTACK_ABSENT:-fallback}"` + "\n",
			want:  map[string]string{"E": "fallback"},
		},
		{
			name:   "interpolation of a present var",
			envrc:  `export E="${DEVSTACK_HOST:-fallback}/api"` + "\n",
			parent: map[string]string{"DEVSTACK_HOST": "http://real"},
			want:   map[string]string{"E": "http://real/api"},
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

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
