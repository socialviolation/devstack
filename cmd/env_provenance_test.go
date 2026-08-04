package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/svcconfig"
)

func provenanceLadder() []config.EnvLayer {
	return []config.EnvLayer{
		{Rung: config.RungEnvrc, Source: ".envrc", Values: map[string]string{
			"DB":       "envrc-db",
			"ONLY_RC":  "rc",
			"PASSWORD": "hunter2",
		}},
		{Rung: config.RungWorkspaceFiles, Source: "shared.env", Values: map[string]string{"DB": "ws-file-db"}},
		{Rung: config.RungServiceValues, Source: "service.yaml", Values: map[string]string{"DB": "svc-db"}},
		{Rung: config.RungActiveEnv, Source: "perf", Values: map[string]string{"DB": "perf-db"}},
		{Rung: config.RungManaged, Values: map[string]string{"OTEL": "on"}},
	}
}

func rowByKey(t *testing.T, rows []envRow, key string) envRow {
	t.Helper()
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no row for key %q", key)
	return envRow{}
}

func TestBuildEnvRowsAttributesWinningRung(t *testing.T) {
	rows := buildEnvRows(provenanceLadder(), false)

	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	if !reflect.DeepEqual(keys, []string{"DB", "ONLY_RC", "OTEL", "PASSWORD"}) {
		t.Fatalf("keys = %v, want sorted DB ONLY_RC OTEL PASSWORD", keys)
	}

	db := rowByKey(t, rows, "DB")
	if db.Value != "perf-db" || db.Rung != config.RungActiveEnv || db.Source != "active env (perf)" {
		t.Errorf("DB = %+v, want the active env layer to win", db)
	}

	rc := rowByKey(t, rows, "ONLY_RC")
	if rc.Value != "rc" || rc.Rung != config.RungEnvrc || rc.Source != ".envrc" {
		t.Errorf("ONLY_RC = %+v, want the .envrc layer", rc)
	}
	if len(rc.Shadowed) != 0 {
		t.Errorf("ONLY_RC shadowed = %+v, want none", rc.Shadowed)
	}

	otel := rowByKey(t, rows, "OTEL")
	if otel.Rung != config.RungManaged || otel.Source != "devstack-computed" {
		t.Errorf("OTEL = %+v, want devstack-computed", otel)
	}
}

func TestBuildEnvRowsMasksSecrets(t *testing.T) {
	rows := buildEnvRows(provenanceLadder(), false)
	if got := rowByKey(t, rows, "PASSWORD").Value; got != svcconfig.MaskedValue {
		t.Errorf("PASSWORD value = %q, want it masked", got)
	}
}

func TestBuildEnvRowsKeepsConnectionTargetVisible(t *testing.T) {
	layers := []config.EnvLayer{{Rung: config.RungActiveEnv, Source: "perf", Values: map[string]string{
		"ConnectionStrings__App": "Server=db.example.com;Initial Catalog=appdb;Password=hunter2",
	}}}
	got := buildEnvRows(layers, false)[0].Value
	if !strings.Contains(got, "db.example.com") || !strings.Contains(got, "appdb") {
		t.Errorf("value = %q, want the server and database still visible", got)
	}
	if strings.Contains(got, "hunter2") {
		t.Errorf("password leaked in %q", got)
	}
}

func TestBuildEnvRowsRevealPrintsSecrets(t *testing.T) {
	rows := buildEnvRows(provenanceLadder(), true)
	if got := rowByKey(t, rows, "PASSWORD").Value; got != "hunter2" {
		t.Errorf("PASSWORD with reveal = %q, want the raw value", got)
	}
}

func TestBuildEnvRowsListsShadowedLayers(t *testing.T) {
	rows := buildEnvRows(provenanceLadder(), false)
	db := rowByKey(t, rows, "DB")

	want := []envShadow{
		{Rung: config.RungEnvrc, Source: ".envrc", Value: "envrc-db", By: "workspace env.files (shared.env)"},
		{Rung: config.RungWorkspaceFiles, Source: "workspace env.files (shared.env)", Value: "ws-file-db", By: "service env.values"},
		{Rung: config.RungServiceValues, Source: "service env.values", Value: "svc-db"},
	}
	if !reflect.DeepEqual(db.Shadowed, want) {
		t.Errorf("DB shadowed =\n%+v\nwant\n%+v", db.Shadowed, want)
	}
}

func TestBuildEnvRowsShadowedMasksSecrets(t *testing.T) {
	layers := []config.EnvLayer{
		{Rung: config.RungEnvrc, Source: ".envrc", Values: map[string]string{"PASSWORD": "hunter2"}},
		{Rung: config.RungActiveEnv, Source: "perf", Values: map[string]string{"PASSWORD": "swordfish"}},
	}
	rows := buildEnvRows(layers, false)
	sh := rowByKey(t, rows, "PASSWORD").Shadowed
	if len(sh) != 1 || sh[0].Value != svcconfig.MaskedValue {
		t.Errorf("shadowed = %+v, want a single masked entry", sh)
	}
}

// A stack env overriding only some of the workspace env's keys must leave the
// rest credited to the workspace env, and show the buried one as shadowed by it.
func TestBuildEnvRowsSeparatesActiveEnvScopes(t *testing.T) {
	layers := []config.EnvLayer{
		{Rung: config.RungActiveEnv, Source: "dev", Values: map[string]string{"A": "dev-a", "B": "dev-b"}},
		{Rung: config.RungActiveEnv, Source: "perf", Values: map[string]string{"B": "perf-b"}},
	}
	rows := buildEnvRows(layers, false)

	a := rowByKey(t, rows, "A")
	if a.Value != "dev-a" || a.Source != "active env (dev)" {
		t.Errorf("A = %+v, want it credited to active env (dev)", a)
	}
	if len(a.Shadowed) != 0 {
		t.Errorf("A shadowed = %+v, want none", a.Shadowed)
	}

	b := rowByKey(t, rows, "B")
	if b.Value != "perf-b" || b.Source != "active env (perf)" {
		t.Errorf("B = %+v, want it credited to active env (perf)", b)
	}
	want := []envShadow{{Rung: config.RungActiveEnv, Source: "active env (dev)", Value: "dev-b"}}
	if !reflect.DeepEqual(b.Shadowed, want) {
		t.Errorf("B shadowed = %+v, want %+v", b.Shadowed, want)
	}
}

func TestAnyShadowed(t *testing.T) {
	if anyShadowed(buildEnvRows([]config.EnvLayer{{Rung: config.RungEnvrc, Values: map[string]string{"A": "1"}}}, false)) {
		t.Error("anyShadowed = true for a single layer, want false")
	}
	if !anyShadowed(buildEnvRows(provenanceLadder(), false)) {
		t.Error("anyShadowed = false for a ladder with a buried key, want true")
	}
}

func TestEnvSourceLabelNamesFilesAndEnvs(t *testing.T) {
	cases := []struct {
		layer config.EnvLayer
		want  string
	}{
		{config.EnvLayer{Rung: config.RungEnvrc, Source: ".envrc"}, ".envrc"},
		{config.EnvLayer{Rung: config.RungServiceFiles, Source: "local.env"}, "service env.files (local.env)"},
		{config.EnvLayer{Rung: config.RungWorkspaceValues, Source: "workspace.yaml"}, "workspace env.values"},
		{config.EnvLayer{Rung: config.RungActiveEnv, Source: "perf"}, "active env (perf)"},
		{config.EnvLayer{Rung: config.RungActiveEnv}, "active env"},
		{config.EnvLayer{Rung: config.RungManaged}, "devstack-computed"},
	}
	for _, c := range cases {
		if got := envSourceLabel(c.layer); got != c.want {
			t.Errorf("envSourceLabel(%+v) = %q, want %q", c.layer, got, c.want)
		}
	}
}

func TestUnknownEnvErrorExplainsBase(t *testing.T) {
	m := &config.WorkspaceManifest{Environments: map[string]config.WorkspaceEnvironment{"perf": {}}}
	for _, name := range []string{"base", "default"} {
		err := unknownEnvError(name, "navexa", m)
		if err == nil {
			t.Fatalf("unknownEnvError(%q) = nil, want an error", name)
		}
		msg := err.Error()
		if !strings.Contains(msg, "is not an environment name") || !strings.Contains(msg, "no stack") {
			t.Errorf("unknownEnvError(%q) = %q, want it to explain that it is not an env name", name, msg)
		}
		if !strings.Contains(msg, "devstack env which") {
			t.Errorf("unknownEnvError(%q) = %q, want it to point at devstack env which", name, msg)
		}
	}
}

func TestUnknownEnvErrorKeepsNormalNotFound(t *testing.T) {
	m := &config.WorkspaceManifest{Environments: map[string]config.WorkspaceEnvironment{"perf": {}}}
	msg := unknownEnvError("staging", "navexa", m).Error()
	if !strings.Contains(msg, `environment "staging" is not defined in workspace "navexa"`) || !strings.Contains(msg, "Available: perf") {
		t.Errorf("unknownEnvError(staging) = %q, want the plain not-found error", msg)
	}
	if strings.Contains(msg, "no stack") {
		t.Errorf("unknownEnvError(staging) = %q, want no base explanation", msg)
	}
}

func TestAppliedToLabelsEveryScope(t *testing.T) {
	got := appliedTo(envUsage{
		WorkspaceEnv: "local",
		ServiceEnvs:  map[string]string{"navexa-api": "perf", "navexa-web": "", "worker": "perf"},
		StackEnvs:    map[string]string{"import-review": "perf", "idle": ""},
	})
	want := map[string][]string{
		"local": {"workspace"},
		"perf":  {"service: navexa-api", "service: worker", "stack: import-review"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("appliedTo = %+v, want %+v", got, want)
	}
}

func TestAppliedToSkipsUnused(t *testing.T) {
	got := appliedTo(envUsage{WorkspaceEnv: "perf"})
	if len(got) != 1 {
		t.Errorf("appliedTo = %+v, want only the workspace env", got)
	}
	if appliedLabel(got["staging"]) != "unused" {
		t.Errorf("appliedLabel(nil) = %q, want unused", appliedLabel(got["staging"]))
	}
	if appliedLabel(got["perf"]) != "workspace" {
		t.Errorf("appliedLabel = %q", appliedLabel(got["perf"]))
	}
}

func TestFormatEnvKeys(t *testing.T) {
	if got := formatEnvKeys(nil, 3); got != "(none)" {
		t.Errorf("formatEnvKeys(nil) = %q, want (none)", got)
	}
	if got := formatEnvKeys(map[string]string{"B": "1", "A": "2"}, 3); got != "A, B" {
		t.Errorf("formatEnvKeys = %q, want sorted names", got)
	}
	got := formatEnvKeys(map[string]string{"A": "", "B": "", "C": "", "D": "", "E": ""}, 3)
	if got != "A, B, C, +2 more" {
		t.Errorf("formatEnvKeys = %q, want the first 3 plus a remainder count", got)
	}
}
