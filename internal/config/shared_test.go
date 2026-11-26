package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestSharedLoadsAndNamesTheSharedSet(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{
		"sql": "",
		"api": "needs: [sql]\n",
	}, "shared: [sql]\n")
	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if !rw.IsShared("sql") || rw.IsShared("api") {
		t.Errorf("IsShared(sql) = %v, IsShared(api) = %v, want true and false", rw.IsShared("sql"), rw.IsShared("api"))
	}
	if got := rw.SharedOnly().Services; len(got) != 1 || got["sql"].Name != "sql" {
		t.Errorf("SharedOnly() = %v, want only sql", got)
	}
}

func TestSharedRefusesANameThatIsNotAService(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{"api": ""}, "shared: [ghost]\n")
	_, err := ResolveWorkspace(dir)
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("ResolveWorkspace = %v, want a refusal that names ghost", err)
	}
}

func TestSharedRefusesASharedServiceThatNeedsAStackService(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{
		"sql": "needs: [api]\n",
		"api": "",
	}, "shared: [sql]\n")
	_, err := ResolveWorkspace(dir)
	if err == nil || !strings.Contains(err.Error(), `"sql" needs "api"`) {
		t.Fatalf("ResolveWorkspace = %v, want a refusal that names sql and api", err)
	}
}

func TestSharedAllowsASharedServiceToNeedAnotherSharedService(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{
		"sql":   "needs: [store]\n",
		"store": "",
	}, "shared: [sql, store]\n")
	if _, err := ResolveWorkspace(dir); err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
}

func TestStackManifestKeepsEdgesToSharedServices(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{
		"sql": "", "api": "", "web": "",
	}, "shared: [sql]\ncalls:\n  api: [sql, web]\n")
	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	m, err := GenerateStackManifest(rw, "edges--feat", []string{"api"}, func(s string) string { return "/wt/" + s })
	if err != nil {
		t.Fatalf("GenerateStackManifest: %v", err)
	}
	if got := m.Calls["api"]; !reflect.DeepEqual(got, []string{"sql"}) {
		t.Errorf("Calls[api] = %v, want the edge to the shared sql kept and the edge to web dropped", got)
	}
}

func TestRefTargetsNamesTheServicesAManifestRefersTo(t *testing.T) {
	dir := writeEdgeWorkspace(t, map[string]string{
		"api": "env:\n  values:\n    DB: \"${sql.port.tcp}\"\n    SELF: \"${self.url}\"\n    SUFFIX: \"q${stack.suffix:api}\"\n",
		"sql": "",
	}, "")
	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got := rw.RefTargets("api"); !reflect.DeepEqual(got, []string{"sql"}) {
		t.Errorf("RefTargets(api) = %v, want [sql]", got)
	}
}
