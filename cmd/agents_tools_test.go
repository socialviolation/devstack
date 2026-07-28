package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
	"github.com/socialviolation/devstack/internal/workspace"
)

// An agent finds the tools two ways: the MCP listing, and the block devstack
// writes into AGENTS.md. A tool registered but never named there is one an
// agent reading the file has no reason to try — stack_note shipped that way.
func TestAgentInstructionsNameEveryRegisteredTool(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceManifest(t, root)

	s := server.NewMCPServer("test", "0.0.0")
	ws := &workspace.Workspace{Name: "demows", Path: root}
	nvxmcp.RegisterTools(s, nil, "api", nil, "demows", root, ws)

	resp := s.HandleMessage(context.Background(), json.RawMessage(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var listing struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Result.Tools) < 15 {
		t.Fatalf("only %d tools registered, so this guard proves nothing", len(listing.Result.Tools))
	}

	block := buildAgentInstructions("api", filepath.Join(root, "api"), root, "")
	var missing []string
	for _, tool := range listing.Result.Tools {
		if !strings.Contains(block, "`"+tool.Name+"`") {
			missing = append(missing, tool.Name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("registered but never named in AGENTS.md: %s", strings.Join(missing, ", "))
	}
}

// Commands the block tells an agent to run have to exist, or it teaches an
// invented flag as confidently as a real one.
func TestAgentInstructionsOnlyShowRealTunnelFlags(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceManifest(t, root)
	block := buildAgentInstructions("api", filepath.Join(root, "api"), root, "")

	tunnelFlags := regexp.MustCompile(`devstack tunnel (\w+)[^\n]*?(--[a-z-]+)`)
	found := tunnelFlags.FindAllStringSubmatch(block, -1)
	if len(found) == 0 {
		t.Fatal("the block stopped mentioning tunnel flags, so this guard proves nothing")
	}
	for _, m := range found {
		sub, flag := m[1], strings.TrimPrefix(m[2], "--")
		cmd, _, err := rootCmd.Find([]string{"tunnel", sub})
		if err != nil {
			t.Errorf("block names `devstack tunnel %s`, which is not a command", sub)
			continue
		}
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("block gives `tunnel %s --%s`, which that command does not take", sub, flag)
		}
	}
}

func writeWorkspaceManifest(t *testing.T, root string) {
	t.Helper()
	manifest := `version: 1
workspace:
  name: demows
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
`
	if err := os.MkdirAll(filepath.Join(root, "api"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "devstack.workspace.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
}
