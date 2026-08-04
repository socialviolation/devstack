package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/workspace"
)

// migrateFixture registers one workspace under a HOME of its own, and returns a
// patch that records each run and writes a file when it runs. The file is the
// evidence: a report that changes a file is a report that mutated.
func migrateFixture(t *testing.T) (*server.MCPServer, string, *int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Register(workspace.Workspace{Name: "navexa", Path: root, TiltPort: 10350}); err != nil {
		t.Fatalf("register: %v", err)
	}

	ran := 0
	touched := filepath.Join(root, "the-patch-ran")
	patch := migrate.Patch{
		ID:     "0.0.0-fixture",
		Title:  "Write one file, so a run can be told from a report",
		Rescan: true,
		Detect: func(ws *workspace.Workspace) (bool, string, error) {
			if _, err := os.Stat(touched); err == nil {
				return false, "the fixture patch ran already", nil
			}
			return true, "the fixture patch has not run", nil
		},
		Run: func(ws *workspace.Workspace) (migrate.Result, error) {
			ran++
			if err := os.WriteFile(touched, []byte("ran"), 0644); err != nil {
				return migrate.Result{}, err
			}
			return migrate.Result{
				Changed: true,
				Lines:   []string{"    the fixture patch wrote a file"},
				Items:   []migrate.Item{{Label: ws.Name, Path: ws.Path}},
			}, nil
		},
		Next: func([]migrate.Result) []string { return []string{"NOW COMMIT. Read the diff first."} },
	}

	s := server.NewMCPServer("test", "0.0.0")
	registerMigrateTool(s, []migrate.Patch{patch})
	return s, touched, &ran
}

// The whole value of action="list" is that an agent can read what a migration
// would do before it does it. A list that reaches the run path destroys that,
// and it edits repositories that devstack does not own.
func TestMigrateListRunsNoPatch(t *testing.T) {
	s, touched, ran := migrateFixture(t)

	out := callTool(t, s, "migrate", map[string]string{"action": "list"})
	if *ran != 0 {
		t.Fatalf("action=list ran the patch %d time(s):\n%s", *ran, out)
	}
	if _, err := os.Stat(touched); !os.IsNotExist(err) {
		t.Fatalf("action=list wrote %s (stat err = %v)", touched, err)
	}
	for _, want := range []string{"0.0.0-fixture", "pending", "changes nothing"} {
		if !strings.Contains(out, want) {
			t.Errorf("action=list never states %q:\n%s", want, out)
		}
	}
}

// The NEXT block is the reason this tool exists: it carries the work the tool
// cannot do, and an agent that never sees it stops one step early.
func TestMigrateRunAppliesThePatchAndReturnsTheNextBlock(t *testing.T) {
	s, touched, ran := migrateFixture(t)

	out := callTool(t, s, "migrate", map[string]string{"action": "run"})
	if *ran != 1 {
		t.Fatalf("action=run ran the patch %d time(s), want 1:\n%s", *ran, out)
	}
	if _, err := os.Stat(touched); err != nil {
		t.Fatalf("action=run did not apply the patch: %v", err)
	}
	for _, want := range []string{"devstack runs 1 migrations over 1 workspace", "NEXT", "NOW COMMIT"} {
		if !strings.Contains(out, want) {
			t.Errorf("action=run never states %q:\n%s", want, out)
		}
	}

	again := callTool(t, s, "migrate", map[string]string{"action": "run"})
	if *ran != 1 {
		t.Errorf("the second run applied the patch again:\n%s", again)
	}
	if !strings.Contains(again, "Every migration is applied") {
		t.Errorf("the second run does not say the machine is up to date:\n%s", again)
	}
}

func TestMigrateRejectsAnUnknownAction(t *testing.T) {
	s, _, ran := migrateFixture(t)
	out := callTool(t, s, "migrate", map[string]string{"action": "apply"})
	if *ran != 0 {
		t.Fatalf("an unknown action ran the patch:\n%s", out)
	}
	if !strings.Contains(out, "unknown action") || !strings.Contains(out, "list") || !strings.Contains(out, "run") {
		t.Errorf("an unknown action must be refused, and the real ones named:\n%s", out)
	}
}

// An agent calls this tool blind. What it does to repositories devstack does not
// own has to be in the description, and the annotations have to say that one
// action writes.
func TestMigrateToolDeclaresWhatItChanges(t *testing.T) {
	s, _, _ := migrateFixture(t)
	tool, ok := listTools(t, s)["migrate"]
	if !ok {
		t.Fatal("the migrate tool is not registered")
	}

	desc := tool.Description
	for _, want := range []string{
		"does not own", ".mcp.json", "git worktree", "does not commit", "NEXT",
		"action=\"list\" reads only", "action=\"run\"",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("the description never states %q:\n%s", want, desc)
		}
	}
	action := tool.InputSchema.Properties["action"].Description
	for _, want := range []string{"changes nothing", "writes and deletes files"} {
		if !strings.Contains(action, want) {
			t.Errorf("the action parameter never states %q:\n%s", want, action)
		}
	}

	data, err := json.Marshal(tool.Annotations)
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		ReadOnlyHint    *bool `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
		IdempotentHint  *bool `json:"idempotentHint"`
		OpenWorldHint   *bool `json:"openWorldHint"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		t.Fatal(err)
	}
	if a.ReadOnlyHint == nil || *a.ReadOnlyHint {
		t.Error("migrate writes files, so readOnlyHint must be false")
	}
	if a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Error("migrate deletes files in repositories devstack does not own, so destructiveHint must be true")
	}
	if a.IdempotentHint == nil || !*a.IdempotentHint {
		t.Error("a second run changes nothing, so idempotentHint must be true")
	}
	if a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Error("migrate touches this machine only, so openWorldHint must be false")
	}
}
