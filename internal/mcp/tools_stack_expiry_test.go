package mcp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func TestAgentStackCreateRefusesAtTheQuota(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	ws := guardStore(t,
		stack.Record{Name: "agent-a", Base: "navexa", Owner: "alice", ExpiresAt: expires},
		stack.Record{Name: "agent-b", Base: "navexa", Owner: "bob", ExpiresAt: expires},
		stack.Record{Name: "perf", Base: "navexa"},
	)
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws, AgentLimits{MaxStacks: 2, TTL: time.Hour})...)
	registerStackCreateTool(s, ws)

	out := callAs(t, s, alice, "stack_create", `{"name":"agent-c","repos":"api"}`)
	for _, want := range []string{`"isError":true`, "2 of 2 agent stacks", "agent-a (owner alice", "agent-b (owner bob"} {
		if !strings.Contains(out, want) {
			t.Errorf("quota refusal lacks %q: %s", want, out)
		}
	}
	if strings.Contains(out, "perf") {
		t.Errorf("quota refusal lists an owner stack: %s", out)
	}
}

func TestCollectExpiredDryRunChangesNothing(t *testing.T) {
	ws := guardStore(t,
		stack.Record{Name: "agent-old", Base: "navexa", Owner: "alice", ExpiresAt: time.Now().Add(-time.Hour)},
		stack.Record{Name: "perf", Base: "navexa"},
	)
	before, err := os.ReadFile(workspace.DataDir("navexa") + "stacks.json")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := CollectExpired(ws, time.Now(), true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would remove agent-old") || strings.Contains(out.String(), "perf") {
		t.Errorf("dry run output = %q, want agent-old alone", out.String())
	}
	after, err := os.ReadFile(workspace.DataDir("navexa") + "stacks.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("dry run changed the store:\nbefore %s\nafter  %s", before, after)
	}
}

func TestStackListShowsOwnerAndExpiry(t *testing.T) {
	expires := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	ws := guardStore(t,
		stack.Record{Name: "agent-a", Base: "navexa", Owner: "alice", ExpiresAt: expires},
		stack.Record{Name: "perf", Base: "navexa"},
	)
	s := server.NewMCPServer("test", "0.0.0")
	registerStackListTool(s, ws)

	out := callAs(t, s, owner, "stack_list", `{}`)
	if !strings.Contains(out, "owner:    alice") || !strings.Contains(out, "expires:  "+expires.Local().Format(time.RFC3339)) {
		t.Errorf("stack_list lacks the owner or the expiry: %s", out)
	}
	if strings.Count(out, "owner:") != 1 {
		t.Errorf("stack_list shows an owner for a stack that has none: %s", out)
	}
}

func TestStackExtendIsOwnStackOnly(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	ws := guardStore(t,
		stack.Record{Name: "agent-a", Base: "navexa", Owner: "alice", CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour)},
		stack.Record{Name: "agent-b", Base: "navexa", Owner: "bob", CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour)},
	)
	s := server.NewMCPServer("test", "0.0.0", GuardOptions(ws, DefaultAgentLimits)...)
	registerStackExtendTool(s, ws)

	if out := callAs(t, s, alice, "stack_extend", `{"name":"agent-b","hours":2}`); !strings.Contains(out, "belongs to bob") {
		t.Errorf("alice extended bob's stack: %s", out)
	}
	out := callAs(t, s, alice, "stack_extend", `{"name":"agent-a","hours":200}`)
	if strings.Contains(out, `"isError":true`) {
		t.Fatalf("alice could not extend her own stack: %s", out)
	}
	rec, err := stack.FindStack("navexa", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.ExpiresAt.Equal(created.Add(stack.MaxLifetime)) {
		t.Errorf("expiry = %v, want the %v cap from creation", rec.ExpiresAt, stack.MaxLifetime)
	}
}

func TestCollectExpiredForcesOutADirtyAgentWorktree(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "devstack.service.yaml"), []byte("version: 1\nservice:\n  name: api\nruntime:\n  run:\n    command: go run .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "-f", "."},
		{"-c", "user.email=t@example.com", "-c", "user.name=tester", "commit", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	root := t.TempDir()
	wt := filepath.Join(root, "api")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "agent-old", wt).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "devstack.workspace.yaml"), []byte("version: 1\nworkspace:\n  name: agent-old\n  repoDiscovery:\n    mode: explicit\n    repos:\n      - ./api\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := guardStore(t, stack.Record{
		Name: "agent-old", Base: "navexa", Owner: "alice", Root: root,
		Worktrees: map[string]string{"api": wt},
		ExpiresAt: time.Now().Add(-time.Hour),
	})

	var out bytes.Buffer
	if err := CollectExpired(ws, time.Now(), false, &out); err != nil {
		t.Fatalf("CollectExpired: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "removed worktree "+wt) {
		t.Errorf("gc did not remove the dirty worktree %s:\n%s", wt, out.String())
	}
	if _, err := stack.FindStack("navexa", "agent-old"); err == nil {
		t.Errorf("the expired agent stack still has a record")
	}
}
