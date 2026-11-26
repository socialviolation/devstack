package stack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/socialviolation/devstack/internal/config"
)

type MigratedStack struct {
	Name         string
	Before       []string
	After        []string
	ManifestPath string
	Wrote        bool
	Err          error
}

func StoreFile(workspaceName string) string {
	return storePath(workspaceName)
}

func withoutShared(members []string, shared map[string]bool) []string {
	out := []string{}
	for _, m := range members {
		if !shared[m] {
			out = append(out, m)
		}
	}
	return out
}

func PlanMigration(workspaceName string, shared map[string]bool) ([]MigratedStack, error) {
	recs, err := LoadStore(workspaceName)
	if err != nil {
		return nil, err
	}
	out := make([]MigratedStack, 0, len(recs))
	for _, r := range recs {
		out = append(out, MigratedStack{
			Name:         r.Name,
			Before:       r.Members,
			After:        withoutShared(r.Members, shared),
			ManifestPath: config.WorkspaceManifestPath(r.Root),
		})
	}
	return out, nil
}

func ApplyMigration(workspaceName string, baseRW *config.ResolvedWorkspace, write bool) ([]MigratedStack, bool, error) {
	var out []MigratedStack
	storeWrote := false
	err := withStoreLock(workspaceName, func() error {
		recs, err := LoadStore(workspaceName)
		if err != nil {
			return err
		}
		shared := baseRW.SharedSet()
		for i := range recs {
			rec := &recs[i]
			m := MigratedStack{Name: rec.Name, Before: rec.Members, ManifestPath: config.WorkspaceManifestPath(rec.Root)}
			rec.Members = withoutShared(rec.Members, shared)
			m.After = rec.Members
			m.Wrote, m.Err = regenerateManifest(baseRW, rec, write)
			out = append(out, m)
		}
		if len(recs) == 0 {
			return nil
		}
		data, err := json.MarshalIndent(recs, "", "  ")
		if err != nil {
			return fmt.Errorf("can not encode the stacks store: %w", err)
		}
		if before, err := os.ReadFile(storePath(workspaceName)); err == nil && bytes.Equal(before, data) {
			return nil
		}
		storeWrote = true
		if !write {
			return nil
		}
		return saveStore(workspaceName, recs)
	})
	return out, storeWrote, err
}

func regenerateManifest(baseRW *config.ResolvedWorkspace, rec *Record, write bool) (bool, error) {
	if _, err := os.Stat(rec.Root); err != nil {
		return false, fmt.Errorf("the stack root %s is not there, so devstack writes no manifest", rec.Root)
	}
	manifest, err := config.GenerateStackManifest(baseRW, rec.FullName(), rec.Members, func(s string) string { return rec.Worktrees[s] })
	if err != nil {
		return false, err
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return false, fmt.Errorf("can not encode the stack manifest: %w", err)
	}
	path := config.WorkspaceManifestPath(rec.Root)
	if before, err := os.ReadFile(path); err == nil && bytes.Equal(before, data) {
		return false, nil
	}
	if !write {
		return true, nil
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return false, fmt.Errorf("can not write the stack manifest: %w", err)
	}
	return true, nil
}
