package mcp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
)

func TestStatusShowsTheSharedSetThenEachStack(t *testing.T) {
	ws, _, _ := seedStack(t)
	if err := stack.SetActive("testws", "feat", true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"uiResources":[
			{"metadata":{"name":"testws:db"},"status":{"runtimeStatus":"ok"}},
			{"metadata":{"name":"testws:api:feat"},"status":{"runtimeStatus":"ok"}}]}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	s := server.NewMCPServer("test", "0.0.0")
	registerStatusTool(s, tilt.NewClient(u.Hostname(), port), map[string]string{}, &config.WorkspaceConfig{}, ws)

	out := safetyCallTool(t, s, "status", map[string]any{})
	shared, stackAt := strings.Index(out, "shared set:"), strings.Index(out, "stack feat:")
	if shared < 0 || stackAt < shared {
		t.Fatalf("status must show the shared set, then each stack:\n%s", out)
	}
	if db, api := strings.Index(out, "db "), strings.Index(out, "api "); db < shared || db > stackAt || api < stackAt {
		t.Errorf("db must sit under the shared set and api under stack feat:\n%s", out)
	}
	if strings.Contains(out, "base") {
		t.Errorf("status must not name base:\n%s", out)
	}
	if got := safetyCallTool(t, s, "status", map[string]any{"stack": "base"}); !strings.Contains(got, `"isError":true`) {
		t.Errorf("stack base names no stack, and status must refuse it:\n%s", got)
	}
}
