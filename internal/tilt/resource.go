package tilt

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func SplitResourceName(name, prefix string) (svc, stackNS string, ok bool) {
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return rest[:i], rest[i+1:], true
	}
	return rest, "", true
}

func ResourceMap(resources []UIResource, wsName, stackName string) map[string]UIResource {
	prefix := wsName + ":"
	out := make(map[string]UIResource, len(resources))
	for _, r := range resources {
		svc, ns, ok := SplitResourceName(r.Metadata.Name, prefix)
		if !ok || ns != stackName {
			continue
		}
		out[svc] = r
	}
	return out
}

func ServiceStatus(r UIResource) string {
	if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
		return "disabled"
	}
	switch r.Status.RuntimeStatus {
	case "ok":
		return "running"
	case "pending":
		return "starting"
	case "error":
		return "erroring"
	}
	if r.Status.UpdateStatus == "running" {
		return "building"
	}
	if r.Status.UpdateStatus == "error" {
		return "erroring"
	}
	return "stopped"
}

func EndpointPorts(links []EndpointLink) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(links))
	for _, ep := range links {
		u, err := url.Parse(ep.URL)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil || port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}

func StateReason(r UIResource, all []UIResource) string {
	if w := r.Status.Waiting; w != nil {
		var on, disabled []string
		for _, ref := range w.On {
			if isDisabled(all, ref.Name) {
				on = append(on, ref.Name+" (disabled)")
				disabled = append(disabled, ref.Name)
				continue
			}
			on = append(on, ref.Name)
		}
		reason := strings.TrimSpace(w.Reason + " " + strings.Join(on, ", "))
		self, _ := resourceCopy(r.Metadata.Name)
		for _, d := range disabled {
			svc, stackName := resourceCopy(d)
			reason += fmt.Sprintf(". Start it: devstack service start %s --stack %s, or remove the dependency: devstack dependencies remove %s %s",
				svc, stackName, self, svc)
		}
		return reason
	}
	if ServiceStatus(r) == "erroring" && len(r.Status.BuildHistory) > 0 {
		e := strings.TrimSpace(r.Status.BuildHistory[0].Error)
		if i := strings.IndexByte(e, '\n'); i >= 0 {
			e = strings.TrimSpace(e[:i])
		}
		return e
	}
	return ""
}

func isDisabled(all []UIResource, name string) bool {
	for _, r := range all {
		if r.Metadata.Name == name {
			return r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled"
		}
	}
	return false
}

func resourceCopy(name string) (svc, stackName string) {
	parts := strings.SplitN(name, ":", 3)
	switch len(parts) {
	case 3:
		return parts[1], parts[2]
	case 2:
		return parts[1], "base"
	}
	return name, "base"
}
