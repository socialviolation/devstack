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
		return fmt.Errorf("no copy named: this command changes what is running, and this directory is not a stack and not the replica that base runs from. Name base: stack=\"base\" over MCP, or --stack base in a shell. Workspace %q has no stacks. To create one, run: devstack stack create <name> --repos <svc>", name)
	}
	return fmt.Errorf("no copy named: this command changes what is running, and this directory is not a stack and not the replica that base runs from. Name the copy: stack=\"base\" or stack=\"<name>\" over MCP, or --stack base or --stack <name> in a shell. Stacks in workspace %q: %s", name, strings.Join(names, ", "))
}
