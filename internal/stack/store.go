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
	Note       string            `json:"note,omitempty"`        // what this stack is for, in the author's words
	Log        []NoteEntry       `json:"log,omitempty"`         // dated entries: where the work got to
	Groups     []string          `json:"groups,omitempty"`      // groups this stack was created to cover
	Overlay    []string          `json:"overlay"`               // overlay service names, sorted
	Worktrees  map[string]string `json:"worktrees"`             // service -> worktree path
	Ports      map[string]int    `json:"ports"`                 // service/portKey -> allocated port
	Active     bool              `json:"active,omitempty"`      // folded into the base Tiltfile as namespaced resources
	DaemonPort int               `json:"daemon_port,omitempty"` // legacy per-stack daemon port; unused, kept so old records still parse
	CreatedAt  time.Time         `json:"created_at"`
}

type NoteEntry struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

const (
	NoteEntryMax = 200

	// Appending past this drops the oldest, so a writer that logs every step
	// erases the record it was adding to.
	NoteLogEntries = 5
)

func (r Record) LatestEntry() (NoteEntry, bool) {
	if len(r.Log) == 0 {
		return NoteEntry{}, false
	}
	return r.Log[len(r.Log)-1], true
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
		return nil, fmt.Errorf("can not read the stacks store: %w", err)
	}
	var recs []Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("can not parse the stacks store: %w", err)
	}
	return recs, nil
}

// saveStore writes a base workspace's stacks, creating its data dir if needed.
func saveStore(workspaceName string, recs []Record) error {
	if err := os.MkdirAll(workspace.DataDir(workspaceName), 0755); err != nil {
		return fmt.Errorf("can not create the workspace data directory: %w", err)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("can not encode the stacks store: %w", err)
	}
	if err := os.WriteFile(storePath(workspaceName), data, 0644); err != nil {
		return fmt.Errorf("can not write the stacks store: %w", err)
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

// SetNote records what a stack is for. A branch name says what changed; this
// says why, and is the only field devstack never derives. Clearing the note
// (note == "") also drops the log: the entries are progress against a purpose
// that no longer exists.
func SetNote(base, name, note string) error {
	recs, err := LoadStore(base)
	if err != nil {
		return err
	}
	for i := range recs {
		if recs[i].Name == name {
			recs[i].Note = note
			if note == "" {
				recs[i].Log = nil
			}
			return saveStore(base, recs)
		}
	}
	return fmt.Errorf("stack %q not found in workspace %q", name, base)
}

// Reports appended == false when the entry repeats the last one verbatim, which
// is a no-op rather than an error: the record already says it.
func AppendNote(base, name, text string) (appended bool, entry NoteEntry, err error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return false, NoteEntry{}, fmt.Errorf("the entry is empty: say what changed, in one line")
	}
	if n := len([]rune(text)); n > NoteEntryMax {
		return false, NoteEntry{}, fmt.Errorf("the entry is %d characters, and the limit is %d: write one line on what changed, not the detail behind it", n, NoteEntryMax)
	}

	recs, err := LoadStore(base)
	if err != nil {
		return false, NoteEntry{}, err
	}
	for i := range recs {
		if !strings.EqualFold(recs[i].Name, name) {
			continue
		}
		if last, ok := recs[i].LatestEntry(); ok && strings.EqualFold(last.Text, text) {
			return false, last, nil
		}
		entry = NoteEntry{At: time.Now(), Text: text}
		log := append(recs[i].Log, entry)
		if len(log) > NoteLogEntries {
			log = log[len(log)-NoteLogEntries:]
		}
		recs[i].Log = log
		return true, entry, saveStore(base, recs)
	}
	return false, NoteEntry{}, fmt.Errorf("stack %q not found in workspace %q", name, base)
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

// AnyActive reports whether a base workspace has any stack marked active.
func AnyActive(base string) (bool, error) {
	recs, err := LoadStore(base)
	if err != nil {
		return false, err
	}
	for i := range recs {
		if recs[i].Active {
			return true, nil
		}
	}
	return false, nil
}

// DeactivateAll marks every active stack of a base workspace inactive and
// persists the change, returning the short names of the stacks it deactivated.
// Bringing a base down calls it so no stack record lingers marked active once
// the daemon that ran it is gone.
func DeactivateAll(base string) ([]string, error) {
	recs, err := LoadStore(base)
	if err != nil {
		return nil, err
	}
	var deactivated []string
	for i := range recs {
		if recs[i].Active {
			recs[i].Active = false
			deactivated = append(deactivated, recs[i].Name)
		}
	}
	if len(deactivated) == 0 {
		return nil, nil
	}
	if err := saveStore(base, recs); err != nil {
		return nil, err
	}
	return deactivated, nil
}

// SetEnv sets the active env applied at a base workspace's stack scope and
// persists it. Errors if the stack is unknown.
func SetEnv(base, name, envName string) error {
	recs, err := LoadStore(base)
	if err != nil {
		return err
	}
	for i := range recs {
		if strings.EqualFold(recs[i].Name, name) {
			recs[i].Env = envName
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
		return nil, nil, fmt.Errorf("can not read the current directory: %w", err)
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
	return nil, nil, fmt.Errorf("this directory is not inside a feature stack")
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
