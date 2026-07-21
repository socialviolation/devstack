package stack

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/socialviolation/devstack/internal/workspace"
)

// Record is one feature stack owned by a base workspace. It is the source of
// truth for a stack's existence and everything that makes it visible: the base
// it overlays, where it lives on disk, its overlay service set and worktrees, the
// ports it was allocated, and its own dev daemon port. A stack is NOT a registry
// workspace — these records live in the base workspace's per-workspace store, so
// an agent bound to the base can see every in-flight stack.
type Record struct {
	Name       string            `json:"name"`        // short feature name (e.g. "import-review")
	Base       string            `json:"base"`        // owning base workspace name
	Root       string            `json:"root"`        // synthesised stack root dir (sibling of base)
	Branch     string            `json:"branch"`      // branch the changed repos' worktrees are on
	Overlay    []string          `json:"overlay"`     // overlay service names, sorted
	Worktrees  map[string]string `json:"worktrees"`   // service -> worktree path
	Ports      map[string]int    `json:"ports"`       // service/portKey -> allocated port
	DaemonPort int               `json:"daemon_port"` // this stack's Tilt daemon port
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

// allocateDaemonPort returns a free Tilt daemon port for a new stack: max+1 over
// every registered workspace's TiltPort and every existing stack's daemon port
// (minimum 10350), skipping any candidate already listening. A stack's daemon
// port is not a registry entry, so this is the allocator that keeps stacks and
// base workspaces off each other's ports.
func allocateDaemonPort() (int, error) {
	used := map[int]bool{}
	max := 10349

	all, err := workspace.All()
	if err != nil {
		return 0, err
	}
	for _, w := range all {
		if w.TiltPort != 0 {
			used[w.TiltPort] = true
			if w.TiltPort > max {
				max = w.TiltPort
			}
		}
		recs, err := LoadStore(w.Name)
		if err != nil {
			return 0, err
		}
		for _, r := range recs {
			if r.DaemonPort != 0 {
				used[r.DaemonPort] = true
				if r.DaemonPort > max {
					max = r.DaemonPort
				}
			}
		}
	}

	for c := max + 1; c < max+1+1000; c++ {
		if used[c] || portListening(c) {
			continue
		}
		return c, nil
	}
	return 0, fmt.Errorf("no free daemon port found in range %d-%d", max+1, max+1000)
}

func portListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
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
