package tunnel

import (
	"fmt"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
)

// StackOnBasePorts maps one stack's instances onto the ports base normally
// serves, so the far end reaches the stack at the address it already knows. Each
// forward listens on base's port over there and lands on the stack's port here;
// a service the stack does not overlay is left out, since base already serves it.
func StackOnBasePorts(view *tilt.TiltView, filter map[string]bool, wsName, stackName string) ([]Service, []string, error) {
	rec, err := stack.FindStack(wsName, stackName)
	if err != nil {
		return nil, nil, err
	}

	basePorts := map[string]int{}
	for _, s := range Discover(view, nil, wsName, false) {
		if _, seen := basePorts[s.Service]; !seen {
			basePorts[s.Service] = s.Port
		}
	}

	var out []Service
	var unmapped []string
	for _, s := range Discover(view, filter, wsName, true) {
		if !strings.HasSuffix(s.Name, ":"+rec.Name) {
			continue
		}
		base, ok := basePorts[s.Service]
		if !ok {
			unmapped = append(unmapped, s.Service)
			continue
		}
		s.RemotePort = base
		out = append(out, s)
	}

	if len(out) == 0 {
		return nil, unmapped, fmt.Errorf("stack %q has no running services to forward — bring it up with: devstack stack up %s", rec.Name, rec.Name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RemotePort < out[j].RemotePort })
	return out, unmapped, nil
}
