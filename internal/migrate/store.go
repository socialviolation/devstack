package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/socialviolation/devstack/internal/workspace"
)

// Record is one patch, applied once, in one workspace. It is machine-scoped
// state: it lives beside every other thing devstack knows about this machine,
// and it never enters a repository.
type Record struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace,omitempty"`
	AppliedAt time.Time `json:"appliedAt"`
}

// StorePath is the file that holds every record.
func StorePath() string {
	return filepath.Join(workspace.DataRoot(), "migrations.json")
}

// Load reads every record. A machine that never migrated has no file, which is
// not an error: it is a machine where every patch is pending.
func Load() ([]Record, error) {
	data, err := os.ReadFile(StorePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("devstack can not read %s: %w", StorePath(), err)
	}
	var recs []Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, so devstack does not know what it applied: %w", StorePath(), err)
	}
	return recs, nil
}

// Append writes one record. It reads the file again first, so a second devstack
// that recorded a patch in the meantime keeps its record.
func Append(rec Record) error {
	recs, err := Load()
	if err != nil {
		return err
	}
	recs = append(recs, rec)
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(StorePath()), 0755); err != nil {
		return fmt.Errorf("devstack can not create %s: %w", filepath.Dir(StorePath()), err)
	}
	if err := os.WriteFile(StorePath(), append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("devstack can not write %s: %w", StorePath(), err)
	}
	return nil
}

// appliedAt returns when a patch was applied in one workspace, and the zero time
// when it was not.
func appliedAt(recs []Record, id, ws string) time.Time {
	for _, r := range recs {
		if r.ID == id && r.Workspace == ws {
			return r.AppliedAt
		}
	}
	return time.Time{}
}
