package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func setTokens(t *testing.T, file, env string) {
	t.Helper()
	path := ""
	if file != "" {
		path = filepath.Join(t.TempDir(), "tokens")
		if err := os.WriteFile(path, []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(TokensFileEnv, path)
	t.Setenv(TokensEnv, env)
}

func TestLoadTokensReadsFileAndEnv(t *testing.T) {
	setTokens(t,
		"# owners\n\nowner nick "+hashHex("file-token")+"\n  agent ci "+hashHex("ci-token")+"  \n",
		"agent:bot:a:b:c, owner:me:mine")
	tokens, err := LoadTokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 4 {
		t.Fatalf("got %d tokens, want 4", len(tokens))
	}
	for raw, want := range map[string]Caller{
		"file-token": {Name: "nick", Scope: ScopeOwner},
		"ci-token":   {Name: "ci", Scope: ScopeAgent},
		"a:b:c":      {Name: "bot", Scope: ScopeAgent},
		"mine":       {Name: "me", Scope: ScopeOwner},
	} {
		got, ok := match(tokens, raw)
		if !ok || got != want {
			t.Errorf("match(%q) = %+v, %v; want %+v", raw, got, ok, want)
		}
	}
}

func TestLoadTokensRejectsUnknownScope(t *testing.T) {
	setTokens(t, "admin nick "+hashHex("x")+"\n", "")
	if _, err := LoadTokens(); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Errorf("file: err = %v, want an unknown scope error", err)
	}
	setTokens(t, "", "root:nick:x")
	if _, err := LoadTokens(); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("env: err = %v, want an unknown scope error", err)
	}
}

func TestLoadTokensNoneSet(t *testing.T) {
	setTokens(t, "", "")
	tokens, err := LoadTokens()
	if err != nil || len(tokens) != 0 {
		t.Errorf("LoadTokens() = %v, %v; want none", tokens, err)
	}
}

func TestMatchRejectsWrongToken(t *testing.T) {
	setTokens(t, "", "agent:ci:right")
	tokens, err := LoadTokens()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"wrong", "", "righ", "right "} {
		if _, ok := match(tokens, raw); ok {
			t.Errorf("match(%q) succeeded, want a miss", raw)
		}
	}
}

func TestCallerFromWithoutCallerIsLocalOwner(t *testing.T) {
	want := Caller{Name: "local", Scope: ScopeOwner}
	if got := CallerFrom(context.Background()); got != want {
		t.Errorf("CallerFrom(background) = %+v, want %+v", got, want)
	}
}

func TestStdioToolSeesLocalOwner(t *testing.T) {
	var seen Caller
	s := whoamiServer(&seen)
	s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami"}}`))
	if want := (Caller{Name: "local", Scope: ScopeOwner}); seen != want {
		t.Errorf("stdio tool saw %+v, want %+v", seen, want)
	}
}

func whoamiServer(seen *Caller) *server.MCPServer {
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	s.AddTool(mcp.NewTool("whoami"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		*seen = CallerFrom(ctx)
		return mcp.NewToolResultText(seen.Name), nil
	})
	return s
}

func post(t *testing.T, url, bearer, session, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`

func TestMiddlewareOverHTTP(t *testing.T) {
	setTokens(t, "", "agent:ci-bot:s3cret")
	tokens, err := LoadTokens()
	if err != nil {
		t.Fatal(err)
	}
	var seen Caller
	ts := httptest.NewServer(Middleware(tokens, server.NewStreamableHTTPServer(whoamiServer(&seen))))
	defer ts.Close()

	for name, bearer := range map[string]string{"missing": "", "wrong": "nope"} {
		resp := post(t, ts.URL, bearer, "", initialize)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s token: status = %d, want 401", name, resp.StatusCode)
		}
		if strings.Contains(string(body), "jsonrpc") {
			t.Errorf("%s token: body %q came from mcp-go, want a plain 401", name, body)
		}
	}

	resp := post(t, ts.URL, "s3cret", "", initialize)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d", resp.StatusCode)
	}
	session := resp.Header.Get("Mcp-Session-Id")
	resp = post(t, ts.URL, "s3cret", session, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/call status = %d", resp.StatusCode)
	}
	if want := (Caller{Name: "ci-bot", Scope: ScopeAgent}); seen != want {
		t.Errorf("tool handler saw %+v, want %+v", seen, want)
	}
}
