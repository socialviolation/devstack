package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/workspace"
)

func TestServeAddrDefaultIsLoopback(t *testing.T) {
	got, err := resolveServeAddr(serveCmd.Flags().Lookup("addr").DefValue, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:8765" {
		t.Errorf("default addr = %q, want 127.0.0.1:8765", got)
	}
}

func TestServeAddrRefusesEveryInterface(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8765", "[::]:8765", ":8765"} {
		if _, err := resolveServeAddr(addr, false); err == nil || !strings.Contains(err.Error(), "every interface") {
			t.Errorf("resolveServeAddr(%q) err = %v, want a refusal that says why", addr, err)
		}
	}
}

func TestServeAddrTailnet(t *testing.T) {
	orig := tailnetIPv4
	t.Cleanup(func() { tailnetIPv4 = orig })
	tailnetIPv4 = func() (string, error) { return "100.64.0.7", nil }

	got, err := resolveServeAddr("127.0.0.1:9000", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "100.64.0.7:9000" {
		t.Errorf("tailnet addr = %q, want 100.64.0.7:9000", got)
	}
}

func mcpPost(t *testing.T, client *http.Client, url, session, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func TestServeHTTPListsTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Workspace{Name: "t", Path: t.TempDir()}
	ts := httptest.NewServer(newHTTPHandler(buildMCPServer(ws)))
	defer ts.Close()

	health, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", health.StatusCode)
	}

	resp, body := mcpPost(t, ts.Client(), ts.URL+"/mcp", "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d: %s", resp.StatusCode, body)
	}
	session := resp.Header.Get("Mcp-Session-Id")

	resp, body = mcpPost(t, ts.Client(), ts.URL+"/mcp", session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list status = %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("tools/list body %q: %v", body, err)
	}
	names := map[string]bool{}
	for _, tool := range out.Result.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"environment", "status", "start"} {
		if !names[want] {
			t.Errorf("tools/list lacks %q; got %v", want, names)
		}
	}
}
