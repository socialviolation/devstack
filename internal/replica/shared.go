package replica

import (
	"errors"
	"fmt"

	"github.com/socialviolation/devstack/internal/workspace"
)

type SharedSyncResult struct {
	*SyncResult
	Restarted []string
}

func SyncShared(ws *workspace.Workspace, restart func(service string) (bool, error)) (*SharedSyncResult, error) {
	synced, err := Sync(ws)
	if err != nil {
		return nil, err
	}
	res := &SharedSyncResult{SyncResult: synced}
	var errs []error
	for _, s := range synced.Services {
		if s.Before == s.After {
			continue
		}
		restarted, err := restart(s.Service)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Service, err))
			continue
		}
		if restarted {
			res.Restarted = append(res.Restarted, s.Service)
		}
	}
	return res, errors.Join(errs...)
}
