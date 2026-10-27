package tiltgen

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func TestServeCmdRunsInScopeWithDefaultMemory(t *testing.T) {
	out, err := generateOneWith(t, `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
`, "", Options{SystemdScopes: true})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `serve_cmd="systemd-run --user --scope --quiet --collect --description='devstack svc' -p MemoryMax=4G -p MemorySwapMax=1G -- sh -c './bin/svc'"`
	if !strings.Contains(out, want) {
		t.Errorf("want substring:\n%s\ngot:\n%s", want, out)
	}
	if strings.Contains(out, "CPUQuota") {
		t.Errorf("CPUQuota must be absent when cpu is unset; got:\n%s", out)
	}
}

func TestServeCmdTakesManifestLimits(t *testing.T) {
	out, err := generateOneWith(t, `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
  limits: { memory: 6G, cpu: 200% }
`, "", Options{SystemdScopes: true})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `-p MemoryMax=6G -p MemorySwapMax=1G -p CPUQuota=200% -- sh -c './bin/svc'`
	if !strings.Contains(out, want) {
		t.Errorf("want substring:\n%s\ngot:\n%s", want, out)
	}
	if strings.Contains(out, "MemoryMax=4G") {
		t.Errorf("the manifest memory must replace the default; got:\n%s", out)
	}
}

func TestServeCmdWithoutSystemdIsTheRunCommand(t *testing.T) {
	gens := limitedGens(t, false)
	out, warnings, err := GenerateHost(gens)
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}
	if !strings.Contains(out, `serve_cmd="./bin/svc"`) {
		t.Errorf("want the bare run command; got:\n%s", out)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "systemd user session") {
		t.Errorf("want one warning about the ignored limits; got %q", warnings)
	}
}

func TestLimitsWithSystemdRaiseNoWarning(t *testing.T) {
	_, warnings, err := GenerateHost(limitedGens(t, true))
	if err != nil {
		t.Fatalf("GenerateHost: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("want no warnings; got %q", warnings)
	}
}

func limitedGens(t *testing.T, scopes bool) []WorkspaceGen {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), `version: 1
workspace:
  name: demo
  repoDiscovery:
    mode: explicit
    repos: [./repos/svc]
`)
	write(t, filepath.Join(dir, "repos", "svc", config.ServiceManifestFileName), `version: 1
service:
  name: svc
runtime:
  run: { command: ./bin/svc }
  limits: { memory: 6G }
`)
	rw, err := config.ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	opts := Options{SystemdScopes: scopes}
	return []WorkspaceGen{{Base: rw, BaseOpts: opts}, {Name: "other", Base: rw, BaseOpts: opts}}
}

func TestServeCmdMemoryOnlyHasNoCPUQuota(t *testing.T) {
	got := scopedCommand("x", "devstack svc", config.ServiceLimits{Memory: "2G"})
	want := "systemd-run --user --scope --quiet --collect --description='devstack svc' -p MemoryMax=2G -p MemorySwapMax=1G -- sh -c 'x'"
	if got != want {
		t.Errorf("scopedCommand() =\n%s\nwant\n%s", got, want)
	}
}

func TestScopeDescriptionNamesWorkspaceAndStack(t *testing.T) {
	for _, tc := range []struct{ prefix, name, namespace, want string }{
		{"", "api", "", "devstack api"},
		{"navexa", "api", "", "devstack navexa/api"},
		{"navexa", "api", "perf", "devstack navexa/api:perf"},
	} {
		if got := scopeDescription(tc.prefix, tc.name, tc.namespace); got != tc.want {
			t.Errorf("scopeDescription(%q, %q, %q) = %q, want %q", tc.prefix, tc.name, tc.namespace, got, tc.want)
		}
	}
}

func TestScopedCommandQuotingSurvivesShell(t *testing.T) {
	cmd := `echo 'it'"'"'s' "$HOME" $PORT && printf '%s\n' "a b"`
	scoped := scopedCommand(cmd, "devstack it's", config.ServiceLimits{})

	argv := strings.Replace(scoped, "systemd-run", "printf '%s\\0'", 1)
	out, err := exec.Command("sh", "-c", argv).Output()
	if err != nil {
		t.Fatalf("sh could not parse %q: %v", scoped, err)
	}
	args := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if args[len(args)-1] != cmd {
		t.Errorf("inner command = %q, want %q", args[len(args)-1], cmd)
	}
	if args[len(args)-2] != "-c" || args[len(args)-3] != "sh" {
		t.Errorf("want sh -c before the command; got %q", args)
	}
	found := false
	for _, a := range args {
		if a == "--description=devstack it's" {
			found = true
		}
	}
	if !found {
		t.Errorf("description did not survive quoting; got %q", args)
	}

	lit := starStr(scoped)
	unq, err := strconv.Unquote(lit)
	if err != nil || unq != scoped {
		t.Errorf("Starlark literal %s does not round-trip: %q, %v", lit, unq, err)
	}
}
