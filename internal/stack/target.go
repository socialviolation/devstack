package stack

import (
	"strings"

	"github.com/socialviolation/devstack/internal/workspace"
)

// The directory decides first, and base is the fallback. Standing in a stack's
// worktree or in the replica names that copy; anywhere else means base.
//
// This used to refuse instead of falling back, because base runs the replica and
// a restart typed in a checkout reloads code that is not the code under the
// caller's hands. That reason holds, and it stopped being worth the cost: every
// command typed at the root of a workspace had to name a copy that was the only
// one it could have meant. Each command reports the copy it acted on — a base
// resource has no :stack suffix — so the fallback is visible where it lands.
//
// Base is returned as "", the empty stack name every caller already reads as "no
// stack", so nothing downstream learns a second spelling for it.
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

