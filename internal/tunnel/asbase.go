package tunnel

import (
	"fmt"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
)

func StackOnDeclaredPorts(view *tilt.TiltView, filter map[string]bool, wsName, stackName string, declared config.PortBook) ([]Service, []string, error) {
	rec, err := stack.FindStack(wsName, stackName)
	if err != nil {
		return nil, nil, err
	}

	var out []Service
	var unmapped []string
	for _, s := range Discover(view, filter, wsName, true) {
		if !strings.HasSuffix(s.Name, ":"+rec.Name) {
			continue
		}
		port, ok := declaredPortFor(rec, declared, s)
		if !ok {
			unmapped = append(unmapped, s.Service)
			continue
		}
		s.RemotePort = port
		out = append(out, s)
	}

	if len(out) == 0 {
		return nil, unmapped, fmt.Errorf("stack %q runs no service with a declared port that devstack can forward. To bring it up, run: devstack stack up %s", rec.Name, rec.Name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RemotePort < out[j].RemotePort })
	return out, unmapped, nil
}

func declaredPortFor(rec *stack.Record, declared config.PortBook, s Service) (int, bool) {
	for key, port := range declared[s.Service] {
		if rec.Ports[stack.QualifyPortKey(s.Service, key)] == s.Port {
			return port, true
		}
	}
	return 0, false
}
