package stack

import (
	"strings"

	"github.com/socialviolation/devstack/internal/workspace"
)

func ResolveTarget(ws *workspace.Workspace, name string) (string, error) {
	if name == "base" {
		return "", nil
	}
	if name != "" {
		return name, nil
	}
	if base, rec, err := DetectFromCwd(); err == nil && rec != nil && ws != nil && strings.EqualFold(base.Name, ws.Name) {
		return rec.Name, nil
	}
	return "", nil
}
