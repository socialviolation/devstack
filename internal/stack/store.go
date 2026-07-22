package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/socialviolation/devstack/internal/workspace"
)

// Record is one feature stack owned by a base workspace. It is the source of
// truth for a stack's existence and everything that makes it visible: the base
// it overlays, where it lives on disk, its overlay service set and worktrees, the
// ports it was allocated, and whether it is active (folded into the base
// workspace's one Tiltfile). A stack is NOT a registry workspace — these records
// live in the base workspace's per-workspace store, so an agent bound to the base
// can see every in-flight stack.
type Record struct {
	Name       string            `json:"name"`                  // short feature name (e.g. "import-review")
	Base       string            `json:"base"`                  // owning base workspace name
	Root       string            `json:"root"`                  // synthesised stack root dir (sibling of base)
	Branch     string            `json:"branch"`                // branch the changed repos' worktrees are on
	Env        string            `json:"env,omitempty"`         // active env name applied at the stack scope
	Overlay    []string          `json:"overlay"`               // overlay service names, sorted
	Worktrees  map[string]string `json:"worktrees"`             // service -> worktree path
	Ports      map[string]int    `json:"ports"`                 // service/portKey -> allocated port
	Active     bool              `json:"active,omitempty"`      // folded into the base Tiltfile as namespaced resources
	DaemonPort int               `json:"daemon_port,omitempty"` // legacy per-stack daemon port; unused, kept so old records still parse
	CreatedAt  time.Time         `json:"created_at"`
}

// RuntimeKey is the globally unique key a stack's runtime state is filed under:
// its allocation ledger (ports.json), PID/log files, and session. It matches the
// pre-rekey composite name so the port allocator, session, and daemon plumbing
// are reused unchanged — only the record's home moved out of the registry.
func (r Record) RuntimeKey() string {
	return r.Base + "--" + r.Name
}

// FullName is the human-facing identity of the stack: "<base>--<name>".
func (r Record) FullName() string {
	return r.Base + "--" + r.Name
}

// storePath returns the per-workspace stacks store file.
func storePath(workspaceName string) string {
	return workspace.DataDir(workspaceName) + "stacks.json"
}

// LoadStore reads a base workspace's stacks, returning an empty slice when it has
// none.
func LoadStore(workspaceName string) ([]Record, error) {
	data, err := os.ReadFile(storePath(workspaceName))
	if err != nil {
		if os.IsNotExist(err) {
			return []Record{}, nil
		}
		return nil, fmt.Errorf("failed to read stacks store: %w", err)
	}
	var recs []Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("failed to parse stacks store: %w", err)
	}
	return recs, nil
}

// saveStore writes a base workspace's stacks, creating its data dir if needed.
func saveStore(workspaceName string, recs []Record) error {
	if err := os.MkdirAll(workspace.DataDir(workspaceName), 0755); err != nil {
		return fmt.Errorf("failed to create workspace data dir: %w", err)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal stacks store: %w", err)
	}
	if err := os.WriteFile(storePath(workspaceName), data, 0644); err != nil {
		return fmt.Errorf("failed to write stacks store: %w", err)
	}
	return nil
}

// FindStack returns a base workspace's stack by short name (case-insensitive).
func FindStack(workspaceName, name string) (*Record, error) {
	recs, err := LoadStore(workspaceName)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		if strings.EqualFold(recs[i].Name, name) {
			r := recs[i]
			return &r, nil
		}
	}
	return nil, fmt.Errorf("stack %q not found in workspace %q", name, workspaceName)
}

// SetActive marks a base workspace's stack active or inactive and persists it. An
// active stack's overlay services are folded into the base workspace's Tiltfile as
// namespaced resources; an inactive one is left out. Errors if the stack is unknown.
func SetActive(base, name string, active bool) error {
	recs, err := LoadStore(base)
	if err != nil {
		return err
	}
	for i := range recs {
		if strings.EqualFold(recs[i].Name, name) {
			recs[i].Active = active
			return saveStore(base, recs)
		}
	}
	return fmt.Errorf("stack %q not found in workspace %q", name, base)
}

// upsertStack inserts or replaces a record in its base workspace's store.
func upsertStack(rec Record) error {
	recs, err := LoadStore(rec.Base)
	if err != nil {
		return err
	}
	for i := range recs {
		if strings.EqualFold(recs[i].Name, rec.Name) {
			recs[i] = rec
			return saveStore(rec.Base, recs)
		}
	}
	recs = append(recs, rec)
	return saveStore(rec.Base, recs)
}

// deleteStack removes a record from its base workspace's store.
func deleteStack(workspaceName, name string) (bool, error) {
	recs, err := LoadStore(workspaceName)
	if err != nil {
		return false, err
	}
	for i := range recs {
		if strings.EqualFold(recs[i].Name, name) {
			recs = append(recs[:i], recs[i+1:]...)
			return true, saveStore(workspaceName, recs)
		}
	}
	return false, nil
}

// DetectFromCwd resolves the (base workspace, stack) that owns the current
// directory by matching cwd against every registered workspace's stored stack
// roots and worktree paths. It consults the stacks stores, not the registry,
// because a stack root is a sibling of its base and is never registered.
func DetectFromCwd() (*workspace.Workspace, *Record, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get current directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	all, err := workspace.All()
	if err != nil {
		return nil, nil, err
	}
	for i := range all {
		recs, err := LoadStore(all[i].Name)
		if err != nil {
			return nil, nil, err
		}
		for j := range recs {
			if stackOwnsPath(recs[j], cwd) {
				w := all[i]
				r := recs[j]
				return &w, &r, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("not inside a feature stack")
}

func stackOwnsPath(rec Record, cwd string) bool {
	candidates := make([]string, 0, len(rec.Worktrees)+1)
	candidates = append(candidates, rec.Root)
	for _, wt := range rec.Worktrees {
		candidates = append(candidates, wt)
	}
	for _, base := range candidates {
		if base == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(base); err == nil {
			base = resolved
		}
		if cwd == base || strings.HasPrefix(cwd, base+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}
