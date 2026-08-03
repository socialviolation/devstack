package stack

import (
	"fmt"
	"strings"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

// Erroring on no target is deliberate: base runs from the replica, not from the
// checkouts, so a command typed in a checkout that silently meant base would act
// on code that is not the code under the caller's hands.
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
	if rws, err := replica.DetectFromCwd(); err == nil && ws != nil && strings.EqualFold(rws.Name, ws.Name) {
		return "", nil
	}
	return "", noTargetError(ws)
}

func noTargetError(ws *workspace.Workspace) error {
	name := "?"
	if ws != nil {
		name = ws.Name
	}
	var names []string
	if ws != nil {
		if recs, err := LoadStore(ws.Name); err == nil {
			for _, r := range recs {
				names = append(names, r.Name)
			}
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no instance named: this changes what is running, and this directory is neither a stack nor the replica base runs from. name base — stack=\"base\" over MCP, --stack base in a shell. workspace %q has no stacks (create one with: devstack stack create <name> --repos <svc>)", name)
	}
	return fmt.Errorf("no instance named: this changes what is running, and this directory is neither a stack nor the replica base runs from. name the instance — stack=\"base\" or stack=\"<name>\" over MCP, --stack base or --stack <name> in a shell. stacks in workspace %q: %s", name, strings.Join(names, ", "))
}
